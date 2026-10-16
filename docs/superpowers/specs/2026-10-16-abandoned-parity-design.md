# Abandoned checkouts like kitcommerce-admin

The owner asked for the abandoned cart and checkout screens to be the same as
kitcommerce-admin's. This is the first of two pull requests: the list, the
detail page and the dialogs. The automations list, the workflow editor and the
recovery report follow in the second.

## What the engine can honestly fill

A cart here holds an email, its lines, a subtotal and a discount code. Name,
phone and address only arrive with an order, and payment happens on an order,
never on a cart. So:

- **Name and phone** come from the newest order placed with the basket's email —
  the same reading the Customers screen makes (D22: there are no customer
  records). A first-time shopper shows their email alone; nobody shows as
  "Guest" unless there is no email either.
- **Search** matches the email, or the name or phone on any order placed with it.
- **Cart value** is the subtotal. Shipping and tax are worked out at checkout, so
  the checkout summary shows the subtotal, the discount code and the currency,
  and nothing it would have to invent.
- **No payment card.** A cart never attempts a payment.
- **Thumbnails** are each product's lead picture, the one the product list shows.
- **The recovery link** works while the cart exists, so its expiry is the cart's.

## Screens

- **List.**
  - Header: h1 with a count badge, plus "Report" and "Automations".
  - Six metric cards with icons and sentence-case labels.
  - A filter card with:
    - live search ("Search name, email or phone");
    - a segmented All / Checkouts / Carts;
    - a status multi-select;
    - Any channel / Sent by email;
    - a cart-value popover;
    - a date-range popover.
  - Rows show:
    - the customer on two lines;
    - A's recovery progress;
    - status pills with a dot, in A's colours (scheduled and contacted blue, suppressed red, expired grey).
  - The row menu has:
    - View details;
    - Send recovery;
    - Copy recovery link;
    - Suppress;
    - Open customer;
    - Open cart;
    - Open recovered order.
  - A's empty, filtered and error states.
  - The panel's shared pager stays, so this list pages like every other.
- **Detail.**
  - Header:
    - a back arrow and an h1 "Abandoned checkout" or "Abandoned cart", with the status inline;
    - a subline: id with copy, "Abandoned 3 hours ago", value, items;
    - the buttons Send recovery, Copy recovery link and Suppress, and a More menu.
  - Banners for recovered, expired, nothing scheduled (with its reason) and suppressed.
  - Products card: thumbnails and A's issue chips.
  - Timeline: newest first, grouped by day, with A's labels.
  - Customer card: name, email, phone, orders, lifetime value, last order, copy buttons.
  - Recovery card: A's labels.
  - Checkout summary card.
  - The basket's current state stays: kitcommerce-admin has nothing equivalent, and it is how an operator learns the cart is gone.
- **Dialogs.**
  - Send recovery: A's wording, and channel as a select.
  - Suppress: A's wording, with a 1000-character note.
  - A recovery-link dialog when the browser refuses the copy.

## Not in this pull request

- "View email" on a sent message. It needs the notification preview from #42.
- "View address". A cart has no address.
- Marketing consent. That belongs to the newsletter module, and one module does not read another's tables.
