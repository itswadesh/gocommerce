# Product customizer

A shopper designs their own print on a product, such as a phone case with
their photo, a name, shapes and stickers, and buys it as a line of its own.
This page is the contract a storefront's editor is built against. The
decision behind it is D174 in [`PLAN.md`](../PLAN.md); the exact request and
response shapes are in the OpenAPI document every store serves at `/doc`.

The module is `ext/customizer`, installed with `-customizer`. It is off in
every store until switched on under Settings › Plugins, and a product offers
the editor only once its own Customizer card is switched on.

## The flow

1. Every product says `"designable"` on its payload, in a listing and on
   its own. `true` is sold only made to a design: offer no quick add for it,
   and ask for its setup only then. `false` is a plain product: skip the
   setup request. An engine without the key predates it: ask as before.
   `GET /x/customizer/products/{product_id}` reads the setup. 404 means
   the product cannot be designed after all: draw the ordinary product
   page.
2. Before the shopper chooses a photo, show `consent`.
3. Each photo goes to `POST /x/customizer/uploads`. Keep the `upload_id`.
4. When the shopper adds to bag, render two pictures in the browser and upload
   both:
   - the **preview**: the mockup with the design and the overlay on it, the
     picture the basket, the order and the print operator see;
   - the **print file**: the design alone, clipped to the print area, at
     `print_size` in pixels (below).
5. `POST /x/customizer/designs` saves the design with both uploads. Keep the
   `design_id`.
6. `POST /api/carts/{cartId}/line-items` with `variant_id`, `quantity` and
   `design_id` adds it to the basket.

A design is never edited. A shopper who changes it after adding it saves a new
design and adds that; remove the old line.

## The setup

```json
{
  "product_id": 41,
  "mockup": {"url": "/media/stores/store_…/a1b2….png", "width": 1000, "height": 2000},
  "overlay_url": "/media/stores/store_…/c3d4….png",
  "print_area": {"x": 100, "y": 100, "width": 800, "height": 1600, "radius": 80},
  "print_size": {"width_mm": 75, "height_mm": 150, "dpi": 300},
  "tools": {"upload": true, "text": true, "shapes": true, "background": true, "stickers": true},
  "fonts": [{"key": "inter", "family": "Inter"}, {"key": "poppins", "family": "Poppins"}],
  "stickers": [{"id": 7, "url": "/media/stores/store_…/e5f6….png", "width": 256, "height": 256}],
  "min_photo_px": 1000,
  "consent": "Your photo is used only to print your order. Only upload photos you have the right to print."
}
```

- **Coordinates** are the mockup's pixels as a browser shows it, so a JPEG
  taken on a phone is measured turned the way its EXIF orientation says.
  Draw the mockup from its `url` at any size and scale everything by the
  same factor; a resized copy from the engine's image route is not turned.
- **The overlay** is a transparent PNG the mockup's size, drawn above the
  design: the camera cut-out, the case's edges. `null` when the product has
  none.
- **The print area** is where the design goes, with its corners rounded by
  `radius`. Clip the design to it.
- **The print file** is `round(width_mm / 25.4 × dpi)` by
  `round(height_mm / 25.4 × dpi)` pixels. The print area has the same shape,
  within 2%, so scaling it up by one factor fills the print file. A setup
  never asks for more than 3.8 megapixels or 16384 pixels a side, so the
  print file saved as a PNG from a canvas fits an upload whatever it
  shows, noise with varying transparency included: a 75 × 150 mm case at
  300 dpi is 886 × 1772. A larger print waits for print files rendered on
  the server (below).
- **Fonts** are keys from the theme editor's list. Load each family the way
  the storefront loads the theme's fonts.
- **`min_photo_px`**: a photo whose shorter side is below this is likely to
  print soft. Warn the shopper; the engine does not refuse it.
- A tool that is `false` is not offered, and a design using it is refused.

## Uploads

`POST /x/customizer/uploads` takes one file in a multipart field called
`file` and answers 201 with `{"upload_id", "url", "width", "height"}`.

| | |
|---|---|
| Types | JPEG and PNG. WebP, HEIC and the rest answer 415: draw them into a canvas and send a JPEG (a photo) or a PNG (anything with transparency) |
| Size | 15 MB at most (413), and 40 megapixels and 16384 pixels a side at most (422). A picture that would take more than 256 MB to decode, turn and write again is refused too (422), with what to do in the message: a 16-bit PNG that large is to be saved as an 8-bit one, a progressive JPEG as an ordinary one. A 12-megapixel phone photo, ordinary or progressive, is well inside it |
| Time | The file may take up to 3 minutes to arrive, as long as no 15 seconds pass without a byte; otherwise the answer is 400 `upload_too_slow`, which a shopper can act on by moving to a steadier connection |
| What is kept | The picture, decoded and written again: EXIF and its GPS position are dropped, a JPEG is first turned the way its camera was held, so `width` and `height` are the picture as it is seen, and a 16-bit PNG is kept at 8 bits |
| Where | The store's media storage under a random name, not the media library. While it arrives and is decoded it is a file in the engine's temporary directory, which must be writable ([operations](operations.md#in-a-container)) |
| Busy | Uploads are decoded one at a time. When eight are already waiting, or this one has waited 30 seconds, the answer is 503 `busy` with `Retry-After`, and nothing is kept: send it again then |
| Limit | 30 an hour per address (429, with `Retry-After`) |
| Kept for | 30 days, then deleted, unless a design in a basket or on an order names it |

The limit counts by the request's address, as every limit in the engine
does, and never reads `X-Forwarded-For` itself. Behind a reverse proxy or a
storefront that calls the engine for its shoppers, that address is the
proxy's until the install names it in `GOCOMMERCE_TRUSTED_PROXIES` (engine
PR #191): the engine then counts the shopper the proxy forwarded for, so
this module needs nothing of its own. Raise the limits only where many real
shoppers share one address, such as a mobile carrier's NAT:
`GOCOMMERCE_CUSTOMIZER_UPLOADS_PER_HOUR` and
`GOCOMMERCE_CUSTOMIZER_DESIGNS_PER_HOUR` (30 each when unset; anything but a
whole number above 0 stops the engine at start), or `Config.UploadsPerHour`
and `Config.DesignsPerHour` for an engine built in Go.

## The design document

```json
{
  "product_id": 41,
  "variant_id": 97,
  "preview_upload_id": "…",
  "print_upload_id": "…",
  "document": {
    "version": 1,
    "background": "#f4e1d2",
    "layers": [
      {"id": "p1", "type": "image", "x": -40, "y": 0, "width": 880, "height": 1600, "rotation": 0, "opacity": 1,
       "upload_id": "…"},
      {"id": "t1", "type": "text", "x": 100, "y": 1400, "width": 600, "height": 120, "rotation": -4, "opacity": 1,
       "text": "Asha", "font": "poppins", "color": "#ffffff", "font_size": 96, "align": "center"},
      {"id": "s1", "type": "shape", "x": 60, "y": 60, "width": 120, "height": 120, "rotation": 0, "opacity": 0.8,
       "shape": "heart", "color": "#e11d48"},
      {"id": "k1", "type": "sticker", "x": 600, "y": 80, "width": 160, "height": 160, "rotation": 12, "opacity": 1,
       "sticker_media_id": 7}
    ]
  }
}
```

- Coordinates are the mockup's pixels from the print area's top left corner.
  A layer may reach past the print area; the print area clips it.
- `x` and `y` are the top left corner of the layer's box before it turns.
  Layers are drawn in order, the first at the bottom. `rotation` is degrees
  clockwise about the box's centre, -360 to 360; `opacity` is 0 to 1. The
  engine checks these and never draws them, so the editor and anything that
  later renders the print agree by this page.
- Each type carries only its own fields:

  | Type | Fields | Needs the tool |
  |---|---|---|
  | `image` | `upload_id` | `upload` |
  | `text` | `text` (1 to 200 characters), `font`, `color`, `font_size`, `align` (`left`, `center`, `right`; optional) | `text` |
  | `shape` | `shape` (`rect`, `circle`, `heart`, `star`), `color` | `shapes` |
  | `sticker` | `sticker_media_id`, one of the setup's stickers | `stickers` |

  `background` needs the `background` tool. Colours are `#rrggbb`.
- At most 50 layers and 64 KB. An unknown field anywhere is refused, so a
  version 2 document is a new version, not extra fields on this one.
- Every upload must be one of this store's and not yet deleted (above).
- The print file must be `print_size` in pixels, a pixel either way for the
  editor's rounding, or the design is refused (400).

`POST /x/customizer/designs` answers 201 with `{"design_id", "preview_url"}`,
and is limited to 30 an hour per address.

## The basket and the order

The line-items route takes `design_id` beside `variant_id`. A designed line is
a line of its own: two designs of one variant are two lines, the same design
added again adds to its line, and changing its quantity keeps the design. The
line carries `design_id` and `design_preview_url`.

| Answer | When |
|---|---|
| 404 | No such design in this store |
| 400 | The design was made for another variant |
| 409 | The design is on an order already (save it again to order it again), the product is no longer made to a design, or the store takes no designs |

A line without `design_id` for a product that is `designable` is refused with
409 and the code `design_required`, its own so the storefront can say
"design this one first" and open the editor. A line that went in before the
product's customizer was switched on stays, and checks out as it is.

Two answers about the basket itself tell a storefront to start a fresh one,
each with its own code: `cart_not_found` (404) and `cart_checked_out` (409).

Checkout asks the same questions again and refuses the same way before the
order exists. Each designed order line keeps `metadata.design`:

```json
{
  "id": "…",
  "preview_url": "…",
  "print_url": "…",
  "document": {"version": 1, "background": "#f4e1d2", "layers": ["…"]},
  "photos": [{"upload_id": "…", "url": "…", "width": 3000, "height": 4000}],
  "fonts": [{"key": "poppins", "family": "Poppins"}],
  "print_size": {"width_mm": 75, "height_mm": 150, "dpi": 300},
  "print_area": {"x": 100, "y": 100, "width": 800, "height": 1600, "radius": 80}
}
```

It is a copy, so a later change to the product's setup, or the design being
deleted, never changes a placed order. `print_size` and `print_area` are the
setup's as they were when the design was saved, the size its print file was
made for. The panel's order page shows the preview, the print file to
download with its size, the original photos and the words and fonts used.

## Privacy and retention

- A shopper's files are in no list anyone can read. Their addresses are
  random, and only the design, the basket line and the order line carry them.
  Custom reports cannot read the module's tables.
- Nothing is sent to a third party.
- An upload or a design that no basket holds and no order names is deleted 30
  days after it was made or last added to a basket. A design on an order is
  kept, with its photos, preview and print file, for as long as the order
  line exists.
- An upload or a design of a suspended store is deleted the same way.
- Deleting a store deletes its rows. On the engine's own disk (`-media-dir`)
  the store's folder goes too, and with it every file of the module's. On
  any other media store an embedder plugs in, the module's files are left
  behind: a store's rows are gone before a module hears of its deletion, so
  nothing names them any more, and they are removed by the storage's own
  means, under the store's prefix.

## Not yet

Templates, the "same design" add-ons (a grip or a keychain printed to match),
rendering the print file on the server, and a customization fee are later
phases. A print over 3.8 megapixels waits for the print file rendered on
the server: a browser's PNG of one can be larger than an upload takes. The
data is shaped for them: a design names its product and variant, so a
matching add-on is a design of its own made from the same document.
