# Conversion features: order bump, cross-sells and the basket's offers

The owner asked (2026-10-27) for "order bump feature, cross sell, and other
additional features" that a reference Indian store's product page has. This is
what that store does, what GoCommerce already had, what this round builds, and
a short design for each gap left. Decision D133.

## What the reference store does

Read from its pages without placing an order (a phone case, a night light, the
cart and the checkout):

| # | Where | Feature |
|---|---|---|
| 1 | Product | **Complete the set**: checkboxes under the buy box, "Add Same Design Phone Grip ~~₹129~~ +₹49". The struck price is the grip's own and the "+₹49" what it adds when bought with the case. Ticking adds it with the case. |
| 2 | Product | **Device picker**: "Choose brand" then "Choose model", hundreds of models. |
| 3 | Product | **"30+ sold" / "45.4k+ sold"**, a count rounded down to a band. |
| 4 | Product | **Highlights**: six short ticked lines beside the buy box. |
| 5 | Product | **Freebie** note: a free gift that comes with the product. |
| 6 | Product | **Video** beside the photos. |
| 7 | Product | **Sticky add-to-cart bar** on the phone, with the price. |
| 8 | Product | **You may also like** (cheap accessories, one-tap Add to cart that becomes "1 in cart") and **Related products**. |
| 9 | Cart | **"Add ₹270 more for FREE delivery"**. |
| 10 | Cart, checkout | **Offers**: public codes, each with "Add ₹171 more to use this" and APPLY, "SHOW ALL OFFERS (4)". |
| 11 | Cart | **You may be interested in**: a carousel with a + quick add. |
| 12 | Cart | **Savings**: "Saving ₹1,070", item total against MRP, discount on MRP, delivery, total, a sticky checkout bar. |
| 13 | Checkout | **One page**: a 6-digit PIN code that fills city and state, "Ordered before? Login with OTP", "Save address as Home / Work / Other", codes in checkout, "Place order · ₹189". |

## What GoCommerce had, and what this round does

| # | Before | Now |
|---|---|---|
| 1 | Nothing. | **Built.** Add-ons per product with an optional offer price the engine keeps (below). Panel: a Complete the set card in the product editor. Storefront: checkboxes in the buy box. |
| 2 | Variants with any number of options, and the storefront's picker cross-filters values against real variants, so "Brand" then "Model" works as two options. | **Built (D134).** An option with more than 12 values is a searchable picker; values are judged against the earlier options only, so the brand can change after a model is chosen. |
| 3 | Nothing. | **Built (D134):** Sold count, off by default. |
| 4 | Metafields and category specifications, drawn as a table. | **Built (D134):** a Highlights card, a ticked list in the buy box. |
| 5 | Nothing. | **Built (D134):** a free gift note under the price. |
| 6 | The storefront's gallery plays MP4 and YouTube; the connector passes only pictures. | **Built (D134):** the public product carries its media and the connector passes the videos. |
| 7 | A sticky Add to bag on the phone, without the price. | **Built**: the price is on the bar. |
| 8 | Related products (category, then collections, then newest), drawn without cart buttons. | **Built.** Curated cross-sells per product, a "You may also like" product-page section in the theme editor, cards with a one-tap Add that becomes "N in cart". Related products stay as they were. |
| 9 | Free-over rates existed (a rate band from a subtotal at price 0), but nothing told the shopper. | **Built.** `GET /api/carts/{cartId}/free-shipping`, read from the rates; a bar in the cart. |
| 10 | Codes could be applied, never listed: a list would hand every shopper every code. | **Built.** A code the shop marks "Show in the basket's offers" is listed, judged against the basket. Shown in the cart and the payment step. |
| 11 | Nothing. | **Built.** The basket's products' cross-sells, less what it holds, falling back to the first item's related products. |
| 12 | A "You saved" banner and a sticky checkout bar existed, but a cart line had no MRP, so only a code's saving showed. | **Built**: cart lines carry the variant's compare-at price, so MRP and the saving on it show per line and in the summary. |
| 13 | Sign-in by phone code (D116) and codes at the payment step existed. | **Built (D134):** PIN lookup from a directory file the install provides, and Home / Work / Other in the address book. |

## The order bump (add-ons)

- **Model.** `product_add_ons (product_id, variant_id, price_minor NULL, position)`,
  at most ten per product, unique per variant, never a variant of the product
  itself. `price_minor` NULL means "suggest it at its own price".
- **The engine owns the price.** An add-on is bought on its own basket line
  (`cart_line_items.add_on_id`). Its price is in `cartLinePriceSQL`, the one
  expression the basket, the checkout and every reprice read: the offer price,
  but never above the buyer's own price, while the product it is offered with
  has at least as many units on ordinary lines in the same basket. Otherwise
  the line costs the variant's own.
- **Settling.** Adding one without its product, or past that count, is a 409.
  `UpdateLine` and `RemoveLine` settle add-on lines in their own transaction:
  the excess moves to the variant's ordinary line, at its own price.
- **Withdrawal.** `add_on_id` is not a foreign key. Saving the list keeps the id
  of every add-on that stays; one removed leaves baskets holding a line that
  now reads `price_changed`, and checkout refuses until the shopper has seen it.
- **API.** `PUT /api/admin/products/{id}/add-ons` replaces the list;
  `GET /api/add-ons?product_id=` lists what can be bought;
  `POST /api/carts/{cartId}/line-items {"add_on_id"}` adds one.

## Cross-sells

`product_cross_sells (product_id, related_product_id, position)`, at most 24.
`GET /api/cross-sells?product_id=` and `GET /api/carts/{cartId}/cross-sells`
answer published products only, in the shop's order. The product page's
"You may also like" is a section type (`cross-sells`, product page only, an
optional heading) in the starting product template; with nothing curated it
draws nothing, because Related products already fills in. The basket's
carousel falls back to the first item's related products in the connector.

## The second round (D134)

Each design below was built as written, except:

- **3.** The bands are tens under 100, hundreds under 1,000 and thousands
  above, so "30+ sold" reads as the reference store's does; the setting is on
  Themes › Storefront extras, where the storefront's other extras are.
- **13a.** The engine ships no directory: `GOCOMMERCE_POSTCODES_FILE` points at
  the CSV data.gov.in publishes, and without it nothing is filled. No state is
  guessed from the PIN's digits, because the postal circles do not follow
  state borders.
- **Store-wide basket suggestions** are on Storefront extras too, with a
  collection picker.

## Remaining gaps, each with a design

**2. Long option lists.** The picker draws every value as a pill; 300 phone
models is a wall. Design: the storefront draws an option with more than 12
values as a searchable select, filtered by the values already chosen (the
cross-filter already exists). No engine change.

**3. Sold count.** Design: a built-in setting on its own screen (Settings →
Storefront): off by default, with a threshold (default 25). The engine counts
units on orders that are confirmed and not cancelled or refunded, per product,
refreshed by a ticker into `product_sales_counts`, and answers
`sold_at_least` on the public product, rounded down to 10, 25, 50, 100, 500,
1000 and then whole thousands, and only at or above the threshold. Absent
otherwise: never a number nobody counted.

**4. Highlights.** Design: up to eight short lines on the product
(`metadata.highlights`, plain text, 80 characters each), a card in the editor;
the connector maps them to `highlights` and the buy box draws a ticked list.

**5. Free gift.** Decided as a text badge first (`metadata.free_gift`, plain
text, 80 characters), because a real free line needs stock and fulfilment of a
0-price item, which is an add-on with an offer price of zero that adds itself;
that can follow once a shop asks for stock to be tracked on the gift.

**6. Product video.** Design: the connector passes media of kind video (an
uploaded MP4, or a YouTube link kept as a URL) into the gallery list, after the
pictures; the gallery already plays both.

**13a. PIN code lookup.** Design: an India-only lookup table shipped with the
engine (PIN prefix to state is fixed by the postal circles; the city needs the
post-office directory, about 19,000 rows from data.gov.in under its open
licence) behind `GET /api/postcodes/IN/{pin}`; the address form fills state
and city when the six digits are typed, and the shopper can still change them.

**13b. Address label.** Design: the connector already carries an address's
`label`; the address form adds Home / Work / Other chips that set it.

**Store-wide basket suggestions.** Design: a collection chosen in Settings →
Storefront fills the basket's carousel after the curated cross-sells, before
the related fallback.
