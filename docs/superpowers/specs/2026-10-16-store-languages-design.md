# Each store's languages

**Asked for:** multi-language, queued as item 5 on 2026-10-16. The proposal the
owner queued: languages per store, a place to translate in the panel, and
emails and texts in the customer's language. The panel itself stays English.

## What exists

- **Negotiation:** the engine negotiates a request language (`?lang=`, then
  `Accept-Language`, then the default) against `Config.Languages`, which is
  per install (D21).
- **Orders:** an order records its language (`orders.lang`).
- **Translations:** `ext/translations` stores product translations behind the
  `Translator` port. The shipped binary has no flag for it, and the panel has
  no screen.
- **Message wording:** one per store, channel and event, with no language.
  `Notification.Language` reaches the notifiers, and none of them reads it.
- **`store_profile.language`:** a per-store "language for messages". It never
  decides anything, because every order records a language and the other
  messages are filled from `Config.DefaultLanguage` before they reach it.

## Design

### 1. A store's languages (D87)

- **Storage:**
  - `stores.languages text[]`, with the main language first.
  - NULL means the install's `Config.Languages`, with `Config.DefaultLanguage`
    first. This mirrors `stores.currency` (D85).
- **Reading:**
  - `App.Languages(ctx)` is the only reader.
  - `storeMW` carries the list on the request, beside the currency.
  - `languageMW` now runs inside `storeMW`, so a request is negotiated against
    its own store's languages. Paths without a store use the install's list.
- **Who uses the main language:** everything that read
  `Config.DefaultLanguage` for a store reads the store's main language
  instead:
  - the request fallback;
  - the translator's skip;
  - a notification with no language;
  - store emails (the team invitation, the test email);
  - CSV order import;
  - cms;
  - identity;
  - the MCP `store_info` tool;
  - `/api/admin/settings`.
- **What still uses the install's language:** `/health/ready` and the
  start-up log, which D31 freezes, and an operator's password reset, which
  belongs to a person rather than to a store.
- **Editing, through `PATCH /api/admin/store`:**
  - `languages`, up to 30 tags. Each is normalised: `hi`, `fr-CA`, `zh-Hant`.
    Duplicates are dropped.
  - `language` is the main language. It must be one of the store's languages,
    and saving it moves it to the front.
  - Leaving either out keeps what is stored.
  - Languages can change at any time. Nothing is fixed by orders: an order
    keeps the language it recorded.
- **Public:** `GET /api/store` adds `language` and `languages`, so a
  storefront can draw a language switcher.
- **The old column:** `store_profile.language` is no longer read. Its column
  is marked as unused rather than dropped, because migrations are append-only.

### 2. Translating the catalogue

- **Installing it:** `-translations` installs `ext/translations`, and
  docker-compose turns it on.
- **What gets translated:**
  - Category and collection names gain the seam the module header says they
    lacked: kinds `category` and `collection`, applied on the public category
    and collection reads and where they ride along on a product.
  - Fields: a category's title (its whole path follows), and a collection's
    title and description. Products keep their title, description and SEO
    pair.
- **A list route for the screen:**
  - `GET /api/admin/x/translations/{kind}?language=&q=&status=&page=`
    returns one page of the store's items. Each carries its original fields
    and its translation, plus counts of how many are translated. An item
    counts as translated once its title is.
  - The module reads the catalogue through the services, never the tables.
- **Panel, Products › Translations:**
  - pick a language (the store's non-main languages);
  - see "N of M translated";
  - filter by Not translated / Translated, and search;
  - open an item to see the original beside each field and type the
    translation.
  - Enter saves, as on every other form.

### 3. Messages in the customer's language

- **Storage:** `notification_templates` gains `language`. The key becomes
  `(store_id, channel, event, language)`.
  - Rows written from now on name their language.
  - Existing rows have `''`, which means "written before languages existed"
    and reads as the main language.
- **Choosing the wording for language L:**
  1. L's own row;
  2. the main language's row;
  3. a `''` row;
  4. the code's default.
- **Saving:** saving the main language's wording deletes its `''` row, so one
  wording is stored for it.
- **Changing the main language:** the `''` rows are labelled with the old main
  language first. They were written in it, and otherwise they would follow the
  move into a language they are not written in.
- **API:**
  - `GetIn`, `ListIn`, `SetIn` and `ResetIn` take a language.
  - `Get`, `List`, `Set` and `Reset` keep their meaning (the main language),
    so modules in other repositories still compile.
  - Resend, SendGrid and Twilio render `n.Language`.
- **Customer language:** `MessageLanguage` uses the order's language while the
  store still sells in it, and the main language otherwise.
- **Panel, Email and SMS setup:** a language picker appears when the store has
  more than one language. A language without its own wording says which
  wording its customers get now.

## Out of scope

- **Translated default wording.** The code's defaults stay English. A model's
  Hindi would need a native speaker's review (owner's standards), so the owner
  types each language's wording.
- **MSG91 in other languages.** Each DLT template is registered with its exact
  wording, and per-language template IDs need settings per store language.
  That is a follow-up.
- **The panel in other languages,** and choosing a customer's language on an
  order placed by an operator.

## Owner's to-dos

- Choose each store's languages on Store settings.
- Type the translations and the message wording, or have them reviewed by a
  native speaker.
