# Digital products

Files a buyer downloads once the order is paid: ebooks, templates, presets,
fonts, printables, course material. Decision D254 in `PLAN.md`. Built as the
module `ext/digital`, switched on with `-digital` (it is in
`docker-compose.yml`), plus three small changes to core.

## What a seller does

1. On the product page, press **It is a download only** on the files card (or
   untick **This is a physical product** and stock tracking yourself). Nothing
   is charged to send it, it never sells out, and a basket with nothing else in
   it is asked for no street address.
2. In **Files buyers download**, upload one or more files. A file can belong to
   the whole product or to one variant ("PDF" and "EPUB" as variants, say).
   **Replace** swaps a file's content in place, so everyone who already bought
   it gets the new version; **Remove** takes it away from every buyer.
3. Optionally set a download limit (each buyer may download each file this
   many times) and a number of days the downloads stay open after payment.
   Both empty means no limit and no expiry.

## What a buyer gets

When the order is paid — a gateway's webhook, or **Mark paid** in the panel —
the module grants each paid line every file it has at that moment, marks those
lines fulfilled and delivered, and emails the buyer a link to their order page
when an email provider is set up. The downloads are on the order confirmation
page, on the order tracking page the email links to, and on the order in the
buyer's account. Without an email provider the message is only logged; the
order page still shows the downloads.

## Rules

- **Cash on delivery is refused for a basket with nothing to send.** There is
  no delivery to collect the cash at. A mixed basket may still pay on
  delivery, and its files open when the order is marked paid. An order an
  operator places is let through: that is the seller paid by UPI or bank
  transfer, recording the sale and then marking it paid.
- **Storage is private and in PostgreSQL**, in 1 MiB chunks
  (`digital_chunks`), never under the media directory and never at a URL. So a
  store's files are behind the same row-level security as its orders, are in
  its database backups, and need no extra volume or bucket. The ceiling is
  size: one file is at most 100 MiB. A store selling video courses needs
  object storage, which is a later module behind the same tables.
- **A download link is minted on click** by the order page, for whoever holds
  the order's access token, and lasts ten minutes. It is 32 random bytes, only
  its SHA-256 is stored, and it is good for one file of one order line. The
  first request on a link counts one download against the limit; the browser
  asking again on the same link inside its ten minutes (Android's download
  manager does, and a resumed download does) does not count again.
- **Refunded in full, cancelled, or marked unpaid revokes every grant on the
  order.** A partial refund leaves them open: the money says nothing about
  which line went back. Marked paid again restores grants that "unpaid"
  revoked. An operator can revoke or restore one grant from the API.
- **The engine's own "on its way" and "delivered" messages are not sent for a
  digital parcel** (`SelfAnnouncing`); the module's "Your downloads are ready"
  is. `order.shipped` and `order.delivered` are still raised for every other
  consumer, with `shipment.provider` set to `digital`.
- **Files are served with `Content-Disposition: attachment`, `nosniff`, a
  sandboxing CSP and `no-store`**, and an HTML, SVG, XML or script file is sent
  as `application/octet-stream`, so nothing a seller uploads can run on the
  engine's origin.

## Core changes

- Checkout asks for the full address only when a line is sent; a basket with
  nothing to send needs the country (and the state, which tax and a GST
  invoice are worked out from).
- A cart line carries `requires_shipping`, so a storefront can skip the
  address and delivery steps and say "Instant download".
- `ShipmentEvent` carries the provider, and a provider implementing
  `SelfAnnouncing` sends its own message instead of the engine's.

## API

Public, for the holder of the order's access token (`?token=`):

| Route | What |
|---|---|
| `GET /x/digital/products?ids=1,2` | Which of these products have files, and their formats |
| `GET /x/digital/orders/{number}` | The order's downloads and the state of each |
| `POST /x/digital/orders/{number}/downloads/{id}` | A fresh ten-minute link to one |
| `GET /x/digital/download/{link}` | The file |

Admin, under `/api/admin/x/digital/`: list, upload, replace, remove and fetch a
product's files and set its rule (`catalog.read` / `catalog.write`); list an
order's grants (`orders.read`), revoke and restore one (`orders.write`).
