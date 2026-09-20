package gocommerce

// coreMigrations returns the engine's own schema, applied before any module's.
//
// Migrations are forward-only and append-only: once an ID has shipped, its SQL
// is frozen, and a correction is a new migration. Editing a released migration
// would leave every existing database in a state no future version knows how
// to reason about.
func coreMigrations() []Migration {
	return []Migration{
		{ID: "0001_catalog", SQL: migration0001Catalog},
		{ID: "0002_cart", SQL: migration0002Cart},
		{ID: "0003_orders_and_outbox", SQL: migration0003OrdersAndOutbox},
		{ID: "0004_fulfillment", SQL: migration0004Fulfillment},
		{ID: "0005_superusers", SQL: migration0005Superusers},
		{ID: "0006_merchandising", SQL: migration0006Merchandising},
		{ID: "0007_weight_units", SQL: migration0007WeightUnits},
		{ID: "0008_categories", SQL: migration0008Categories},
		{ID: "0009_variant_media", SQL: migration0009VariantMedia},
		{ID: "0010_cost_and_tax", SQL: migration0010CostAndTax},
		{ID: "0011_customs", SQL: migration0011Customs},
		{ID: "0012_continue_selling", SQL: migration0012ContinueSelling},
		{ID: "0013_roles", SQL: migration0013Roles},
		{ID: "0013_taxonomy_attributes", SQL: migration0013TaxonomyAttributes},
		{ID: "0014_fulfillment_carrier", SQL: migration0014FulfillmentCarrier},
		{ID: "0015_discounts", SQL: migration0015Discounts},
		{ID: "0016_taxes", SQL: migration0016Taxes},
		{ID: "0017_locations", SQL: migration0017Locations},
		{ID: "0018_invitations", SQL: migration0018Invitations},
		{ID: "0019_role_rights", SQL: migration0019RoleRights},
		{ID: "0020_admin_audit", SQL: migration0020AdminAudit},
		{ID: "0021_password_resets", SQL: migration0021PasswordResets},
		{ID: "0022_partial_fulfillment", SQL: migration0022PartialFulfillment},
		{ID: "0023_unsettled_sweep", SQL: migration0023UnsettledSweep},
		{ID: "0024_order_refunds", SQL: migration0024OrderRefunds},
		{ID: "0025_returns", SQL: migration0025Returns},
		{ID: "0026_stock_movements", SQL: migration0026StockMovements},
		{ID: "0027_collection_curation", SQL: migration0027CollectionCuration},
		{ID: "0028_cart_abandonment", SQL: migration0028CartAbandonment},
		{ID: "0029_sort_indexes", SQL: migration0029SortIndexes},
		{ID: "0030_outbox_indexes", SQL: migration0030OutboxIndexes},
		{ID: "0031_shipping", SQL: migration0031Shipping},
		{ID: "0032_variant_dimensions", SQL: migration0032VariantDimensions},
		{ID: "0033_attribute_index", SQL: migration0033AttributeIndex},
		{ID: "0034_customer_groups_and_price_lists", SQL: migration0034Pricing},
		{ID: "0035_channels", SQL: migration0035Channels},
		{ID: "0036_variant_pictures", SQL: migration0036VariantPictures},
		{ID: "0037_requires_shipping", SQL: migration0037RequiresShipping},
		{ID: "0038_plugins", SQL: migration0038Plugins},
		{ID: "0039_notifications", SQL: migration0039Notifications},
		{ID: "0040_notification_templates", SQL: migration0040NotificationTemplates},
		{ID: "0041_role_profiles", SQL: migration0041RoleProfiles},
		{ID: "0042_store_profile", SQL: migration0042StoreProfile},
		{ID: "0043_store_clock", SQL: migration0043StoreClock},
		{ID: "0044_api_keys", SQL: migration0044APIKeys},
		{ID: "0045_custom_reports", SQL: migration0045CustomReports},
		{ID: "0046_api_key_secret", SQL: migration0046APIKeySecret},
		{ID: "0047_vendors", SQL: migration0047Vendors},
		{ID: "0048_vendor_accounts", SQL: migration0048VendorAccounts},
	}
}

// M1 — catalog. A product is the merchandising concept; a variant is the
// sellable unit. Everything downstream (cart lines, order lines, inventory)
// references a variant, so a product with no options still gets exactly one
// default variant and the simple case never becomes a special case.
const migration0001Catalog = `
CREATE TABLE products (
    id          bigserial   PRIMARY KEY,
    slug        text        NOT NULL UNIQUE,
    title       text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'draft'
                            CHECK (status IN ('draft', 'active', 'archived')),
    currency    text        NOT NULL,
    metadata    jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX products_status_idx ON products (status, id DESC);

CREATE TABLE product_options (
    id         bigserial PRIMARY KEY,
    product_id bigint    NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    name       text      NOT NULL,
    position   integer   NOT NULL DEFAULT 0,
    UNIQUE (product_id, name)
);
CREATE INDEX product_options_product_idx ON product_options (product_id, position);

CREATE TABLE product_option_values (
    id        bigserial PRIMARY KEY,
    option_id bigint    NOT NULL REFERENCES product_options (id) ON DELETE CASCADE,
    value     text      NOT NULL,
    position  integer   NOT NULL DEFAULT 0,
    UNIQUE (option_id, value)
);
CREATE INDEX product_option_values_option_idx ON product_option_values (option_id, position);

CREATE TABLE variants (
    id                     bigserial   PRIMARY KEY,
    product_id             bigint      NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    sku                    text        NOT NULL UNIQUE,
    barcode                text,
    price_minor            bigint      NOT NULL CHECK (price_minor >= 0),
    compare_at_price_minor bigint      CHECK (compare_at_price_minor IS NULL OR compare_at_price_minor >= 0),
    -- Inventory lives on the variant because quantity belongs to the sellable
    -- SKU. available = on_hand - reserved, and neither may go negative: the
    -- constraints are the last line of defence behind the reservation service.
    stock_on_hand          integer     NOT NULL DEFAULT 0 CHECK (stock_on_hand >= 0),
    stock_reserved         integer     NOT NULL DEFAULT 0 CHECK (stock_reserved >= 0),
    track_inventory        boolean     NOT NULL DEFAULT true,
    active                 boolean     NOT NULL DEFAULT true,
    weight_grams           integer     CHECK (weight_grams IS NULL OR weight_grams >= 0),
    position               integer     NOT NULL DEFAULT 0,
    -- option_key is the variant's option selection, normalised: the variant's
    -- option_value_ids sorted and joined. A unique index over it is what makes
    -- "no two variants may be Color=Black + Size=M" enforceable in the
    -- database rather than hopeful in the service. A product with no options
    -- has one variant whose key is '', so the same index also guarantees the
    -- single default variant.
    option_key             text        NOT NULL DEFAULT '',
    metadata               jsonb       NOT NULL DEFAULT '{}',
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT variants_reserved_within_on_hand CHECK (stock_reserved <= stock_on_hand)
);
CREATE UNIQUE INDEX variants_product_option_key_idx ON variants (product_id, option_key);
CREATE INDEX variants_product_idx ON variants (product_id, position, id);

CREATE TABLE variant_option_values (
    variant_id      bigint NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    option_value_id bigint NOT NULL REFERENCES product_option_values (id) ON DELETE RESTRICT,
    PRIMARY KEY (variant_id, option_value_id)
);
CREATE INDEX variant_option_values_value_idx ON variant_option_values (option_value_id);
`

// M2 — carts. A cart is guest-owned: its token is the only credential a
// shopper ever has, and no row here references a customer, because guest
// checkout is a permanent guarantee rather than a stage the project grows out
// of.
const migration0002Cart = `
CREATE TABLE carts (
    id         bigserial   PRIMARY KEY,
    token      text        NOT NULL UNIQUE,
    currency   text        NOT NULL,
    status     text        NOT NULL DEFAULT 'open'
                           CHECK (status IN ('open', 'converted', 'abandoned')),
    email      text,
    metadata   jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX carts_expiry_idx ON carts (expires_at) WHERE status = 'open';

CREATE TABLE cart_line_items (
    id         bigserial   PRIMARY KEY,
    cart_id    bigint      NOT NULL REFERENCES carts (id) ON DELETE CASCADE,
    -- Cascade, not restrict: a cart line for a deleted variant cannot be
    -- bought, so it should disappear with it. Restricting instead would let
    -- one abandoned guest cart block an operator from ever removing a
    -- product. Order lines are the opposite case — they keep their snapshot
    -- and merely lose the reference.
    variant_id bigint      NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    quantity   integer     NOT NULL CHECK (quantity > 0),
    -- The price when the line was added. Checkout compares it against the
    -- authoritative price and refuses to silently reprice a confirmed order.
    unit_price_minor bigint NOT NULL CHECK (unit_price_minor >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (cart_id, variant_id)
);
CREATE INDEX cart_line_items_cart_idx ON cart_line_items (cart_id, id);
`

// M3 — orders, idempotency and the transactional outbox.
const migration0003OrdersAndOutbox = `
CREATE TABLE orders (
    id       bigserial PRIMARY KEY,
    number   text      NOT NULL UNIQUE,
    -- The shopper's handle on their own order. Guest checkout means there is
    -- no account to log into, so this token is how an order is looked up.
    access_token text  NOT NULL,
    status   text      NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'confirmed', 'shipped', 'delivered', 'cancelled')),
    payment_status text NOT NULL DEFAULT 'pending'
                       CHECK (payment_status IN ('pending', 'paid', 'failed', 'refunded')),
    payment_provider text NOT NULL,
    payment_reference text,
    currency text        NOT NULL,
    subtotal_minor bigint NOT NULL CHECK (subtotal_minor >= 0),
    shipping_minor bigint NOT NULL DEFAULT 0 CHECK (shipping_minor >= 0),
    discount_minor bigint NOT NULL DEFAULT 0 CHECK (discount_minor >= 0),
    total_minor    bigint NOT NULL CHECK (total_minor >= 0),
    -- The customer as they were at checkout. Historical orders never depend on
    -- mutable customer records for their legal or operational meaning.
    email    text        NOT NULL,
    phone    text,
    name     text,
    address  jsonb       NOT NULL DEFAULT '{}',
    lang     text        NOT NULL DEFAULT 'en',
    metadata jsonb       NOT NULL DEFAULT '{}',
    -- Set while stock is reserved but not yet committed to the sale; the
    -- sweeper uses it to release inventory an abandoned payment is holding.
    reservation_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX orders_status_idx ON orders (status, id DESC);
CREATE INDEX orders_created_idx ON orders (created_at DESC);
CREATE INDEX orders_unpaid_idx ON orders (reservation_expires_at)
    WHERE status = 'pending' AND payment_status = 'pending';

CREATE TABLE order_lines (
    id         bigserial PRIMARY KEY,
    order_id   bigint    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    -- Nullable on purpose: an order line is an immutable snapshot and must
    -- stay readable after the product or variant it came from is deleted.
    product_id bigint    REFERENCES products (id) ON DELETE SET NULL,
    variant_id bigint    REFERENCES variants (id) ON DELETE SET NULL,
    sku        text      NOT NULL,
    title      text      NOT NULL,
    variant_label text   NOT NULL DEFAULT '',
    quantity   integer   NOT NULL CHECK (quantity > 0),
    unit_price_minor bigint NOT NULL CHECK (unit_price_minor >= 0),
    total_minor      bigint NOT NULL CHECK (total_minor >= 0),
    metadata   jsonb     NOT NULL DEFAULT '{}'
);
CREATE INDEX order_lines_order_idx ON order_lines (order_id, id);

-- Same key, same operation, same answer. The unique key is what stops a
-- double-tapped checkout becoming two orders.
CREATE TABLE idempotency_keys (
    id           bigserial   PRIMARY KEY,
    scope        text        NOT NULL,
    key          text        NOT NULL,
    request_hash text        NOT NULL,
    order_id     bigint      REFERENCES orders (id) ON DELETE CASCADE,
    response     jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (scope, key)
);

-- The transactional outbox: an event is written in the same transaction as the
-- state change that caused it, so a crash between commit and publish cannot
-- lose it.
CREATE TABLE outbox_events (
    id             bigserial   PRIMARY KEY,
    event_id       uuid        NOT NULL UNIQUE,
    event_name     text        NOT NULL,
    event_version  integer     NOT NULL DEFAULT 1,
    aggregate_type text        NOT NULL,
    aggregate_id   bigint      NOT NULL,
    payload        jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    available_at   timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz,
    attempts       integer     NOT NULL DEFAULT 0,
    last_error     text,
    dead           boolean     NOT NULL DEFAULT false
);
-- The dispatcher's claim query rides this index; it covers only unpublished
-- rows, so a table of delivered history costs nothing to skip.
CREATE INDEX outbox_unpublished_idx ON outbox_events (available_at, id)
    WHERE published_at IS NULL AND NOT dead;
CREATE INDEX outbox_aggregate_idx ON outbox_events (aggregate_type, aggregate_id, id);
`

// M4 — fulfillment.
const migration0004Fulfillment = `
CREATE TABLE fulfillments (
    id         bigserial   PRIMARY KEY,
    order_id   bigint      NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    provider   text        NOT NULL,
    tracking   text        NOT NULL DEFAULT '',
    label_url  text        NOT NULL DEFAULT '',
    status     text        NOT NULL DEFAULT 'shipped'
                           CHECK (status IN ('shipped', 'delivered', 'cancelled')),
    metadata   jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX fulfillments_order_idx ON fulfillments (order_id, id);
`

// M5 — superusers: the operators who sign in to the admin panel with an email
// and a password.
//
// This is an *operator* table, not a customer table. Nothing in the commerce
// schema references it, and no order, cart or checkout path reads it, so
// guest checkout (D22) stays a property of the engine rather than a mode.
const migration0005Superusers = `
CREATE TABLE superusers (
    id            bigserial   PRIMARY KEY,
    email         text        NOT NULL UNIQUE,
    -- Self-describing PBKDF2: "pbkdf2-sha256$<iterations>$<salt>$<key>". The
    -- cost lives in the row, so raising it later needs no migration and no
    -- flag day; old hashes keep verifying against their own parameters.
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Only the SHA-256 of a session token is stored. A leaked database therefore
-- yields no usable session, and the token itself exists solely in the
-- operator's browser.
CREATE TABLE superuser_sessions (
    token_hash   text        PRIMARY KEY,
    superuser_id bigint      NOT NULL REFERENCES superusers (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX superuser_sessions_user_idx ON superuser_sessions (superuser_id);
CREATE INDEX superuser_sessions_expiry_idx ON superuser_sessions (expires_at);
`

// M6 — merchandising: the product attributes an operator organises a catalog
// by, and the images a storefront renders.
//
// These are the fields every commerce admin has and this one did not: type,
// vendor, tags, collections and media. They are merchandising metadata — the
// commerce state machine does not read any of them, which is why they can be
// added without touching checkout, inventory or orders.
const migration0006Merchandising = `
ALTER TABLE products
    ADD COLUMN product_type text   NOT NULL DEFAULT '',
    ADD COLUMN vendor       text   NOT NULL DEFAULT '',
    -- An array rather than a join table: tags are free text with no identity
    -- of their own, and nothing needs to rename one everywhere at once.
    ADD COLUMN tags         text[] NOT NULL DEFAULT '{}',
    -- The storefront's <title> and meta description. Empty means "derive it
    -- from the title", which is the storefront's call, not the engine's.
    ADD COLUMN seo_title       text NOT NULL DEFAULT '',
    ADD COLUMN seo_description text NOT NULL DEFAULT '';

CREATE INDEX products_type_idx   ON products (product_type) WHERE product_type <> '';
CREATE INDEX products_vendor_idx ON products (vendor)       WHERE vendor <> '';
CREATE INDEX products_tags_idx   ON products USING gin (tags);

-- Collections are a named, ordered grouping an operator curates by hand.
-- Rule-based ("smart") collections are deliberately out: they need a query
-- language, and a hand-picked list is what a small catalog actually uses.
CREATE TABLE collections (
    id          bigserial   PRIMARY KEY,
    slug        text        NOT NULL UNIQUE,
    title       text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    position    integer     NOT NULL DEFAULT 0,
    metadata    jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX collections_position_idx ON collections (position, id);

CREATE TABLE product_collections (
    product_id    bigint  NOT NULL REFERENCES products (id)    ON DELETE CASCADE,
    collection_id bigint  NOT NULL REFERENCES collections (id) ON DELETE CASCADE,
    position      integer NOT NULL DEFAULT 0,
    PRIMARY KEY (product_id, collection_id)
);
CREATE INDEX product_collections_collection_idx
    ON product_collections (collection_id, position, product_id);

-- Media is a store-wide library, not a per-product list. That is what makes
-- "select existing" possible: the same photograph attaches to six products
-- and is stored, and re-encoded, once.
--
-- Rows, not blobs. The engine records where a file is and what it is; the
-- bytes live behind a storage seam, which is what lets a local directory today
-- become object storage later without a schema change.
CREATE TABLE media (
    id   bigserial PRIMARY KEY,
    -- image | video | model — a closed set, because the panel renders each
    -- differently and an unknown kind has no sensible presentation.
    kind text      NOT NULL CHECK (kind IN ('image', 'video', 'model')),
    url  text      NOT NULL,
    -- Empty for media referenced by URL. Set for files this store holds, so
    -- deleting the row can delete the file — and so nothing is ever deleted
    -- from someone else's server.
    storage_key text NOT NULL DEFAULT '',
    filename    text NOT NULL DEFAULT '',
    mime        text NOT NULL DEFAULT '',
    size_bytes  bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    -- Known for images, null for video and models the engine does not decode.
    width  integer CHECK (width  IS NULL OR width  > 0),
    height integer CHECK (height IS NULL OR height > 0),
    alt    text    NOT NULL DEFAULT '',
    metadata   jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX media_kind_idx ON media (kind, id DESC);
CREATE INDEX media_recent_idx ON media (id DESC);

-- What a product shows, in the order it shows it.
CREATE TABLE product_media (
    product_id bigint  NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    -- RESTRICT, not CASCADE: deleting a library item that six products still
    -- display should be refused, not silently strip them all.
    media_id   bigint  NOT NULL REFERENCES media (id) ON DELETE RESTRICT,
    -- Nullable: media belongs to the product, or to one variant of it.
    -- SET NULL so removing a variant does not delete a picture the product
    -- still wants.
    variant_id bigint  REFERENCES variants (id) ON DELETE SET NULL,
    position   integer NOT NULL DEFAULT 0,
    PRIMARY KEY (product_id, media_id)
);
CREATE INDEX product_media_product_idx ON product_media (product_id, position, media_id);
CREATE INDEX product_media_media_idx   ON product_media (media_id);
CREATE INDEX product_media_variant_idx ON product_media (variant_id) WHERE variant_id IS NOT NULL;
`

// M7 — the unit a weight was entered in.
//
// Weight follows money's rule exactly: one canonical stored value, plus the
// unit it should be shown in. `weight_grams` stays the single source of truth,
// because a carrier API wants a real mass and a database that stores "2.5"
// without saying of what is a database nobody can query. `weight_unit` is
// presentation, the way a currency code is — the number is the fact, the unit
// is how a person reads it.
//
// Storing the entered unit rather than only grams is what keeps the round trip
// honest: an operator who types 2.5 kg should see 2.5 kg when they come back,
// not 2500 g, and not 2.5000000001 from a float that went there and back.
const migration0007WeightUnits = `
ALTER TABLE variants
    ADD COLUMN weight_unit text NOT NULL DEFAULT 'g'
        CHECK (weight_unit IN ('g', 'kg', 'oz', 'lb'));
`

// M8 — the product category tree.
//
// A category is not a collection, and the difference is worth stating because
// the two look alike from a distance. A collection is a curated list: "New in",
// six things somebody picked, and a product belongs to as many as an operator
// likes. A category is where a product *sits* in a taxonomy — one place, with a
// parent — which is what a marketplace feed, a tax rule and a shipping profile
// all need to be able to ask. Being singular is the point: "this is a shirt" has
// one answer, and a product filed under both Shirts and Trousers is a data
// error, not a merchandising decision.
//
// Hence the adjacency list plus a single nullable `products.category_id`, rather
// than another join table. Adjacency (a parent pointer) over a materialised path
// or nested sets because the tree is small and edited by hand: a path column
// would have to be rewritten across a whole subtree on every rename, and the one
// query that needs ancestors is a recursive CTE that PostgreSQL runs in
// microseconds at this size.
const migration0008Categories = `
CREATE TABLE categories (
    id bigserial PRIMARY KEY,
    -- NULL is a root. RESTRICT because deleting "Apparel" must not silently
    -- take "Shirts" and "Trousers" with it — the operator is told what is in
    -- the way and moves it first.
    parent_id bigint REFERENCES categories (id) ON DELETE RESTRICT,
    slug      text    NOT NULL UNIQUE,
    title     text    NOT NULL,
    position  integer NOT NULL DEFAULT 0,
    metadata  jsonb   NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Catches the one-node cycle. Longer cycles are unreachable in SQL and are
    -- refused in Go, where the proposed parent's ancestry is walked first.
    CONSTRAINT categories_not_own_parent CHECK (parent_id IS NULL OR parent_id <> id)
);
CREATE INDEX categories_parent_idx ON categories (parent_id, position, id);

ALTER TABLE products
    -- RESTRICT again, and for the same reason as media: deleting a category
    -- that forty products are filed under should be refused with a count, not
    -- quietly uncategorise all forty where nobody will notice.
    ADD COLUMN category_id bigint REFERENCES categories (id) ON DELETE RESTRICT;
CREATE INDEX products_category_idx ON products (category_id) WHERE category_id IS NOT NULL;
`

// M9 — one image per variant.
//
// `product_media.variant_id` has existed since M6 but nothing enforced how many
// rows could point at one variant, and nothing wrote it. Both are fixed here and
// in media.go: a variant shows exactly one of the product's images, which is
// what a storefront swaps to when a shopper picks a colour, and what a feed
// wants when it lists each variant as its own item.
//
// The index is partial because the common case is NULL — most media belongs to
// the product rather than to a variant, and a full unique index would allow
// exactly one unassigned row per product.
const migration0009VariantMedia = `
CREATE UNIQUE INDEX product_media_variant_key
    ON product_media (variant_id) WHERE variant_id IS NOT NULL;
`

// M10 — cost and taxability, the two things a price needs beside it.
//
// Cost is what the item cost the store, and it is the number every margin
// calculation starts from — Shopify's pricing card shows profit and margin
// derived from it, and neither can be computed from a price alone. It is
// nullable because "we have not recorded a cost" is different from "it cost
// nothing", and a zero would quietly report a 100% margin.
//
// `taxable` sits on the variant rather than the product because it is a
// property of the thing being sold: a gift card and a shirt can share a
// product's shape and not its tax treatment. It defaults to true, which is the
// answer for almost everything and the safe one to be wrong about — charging
// tax that is later refunded is recoverable; not charging it is not.
const migration0010CostAndTax = `
ALTER TABLE variants
    ADD COLUMN cost_minor bigint CHECK (cost_minor IS NULL OR cost_minor >= 0),
    ADD COLUMN taxable boolean NOT NULL DEFAULT true;
`

// M11 — customs: where a thing was made, and what a customs officer calls it.
//
// Both belong to the variant rather than the product, for M10's reason: they
// describe the item in the box. A shirt cut in Portugal and the same shirt cut
// in Vietnam are one product and two origins, and a carrier's paperwork asks
// per line, not per listing.
//
// Text rather than an enum. The origin is an ISO 3166-1 alpha-2 code, which
// changes when countries do; the HS code is 6 to 10 digits whose length depends
// on the importing country, so the tariff number for the same shirt is not the
// same string everywhere. Both are validated on the way in and stored as typed.
// Empty means "not recorded", which is what almost every domestic store will
// leave them as.
const migration0011Customs = `
ALTER TABLE variants
    ADD COLUMN origin_country text NOT NULL DEFAULT ''
        CHECK (origin_country = '' OR origin_country ~ '^[A-Z]{2}$'),
    ADD COLUMN hs_code text NOT NULL DEFAULT ''
        CHECK (hs_code = '' OR hs_code ~ '^[0-9]{6,10}$');
`

// M12 — selling past zero.
//
// `track_inventory` already says whether to count at all. This says what to do
// when the count runs out: refuse the sale, which is the default and what every
// store wants for a thing it has to make; or take the order anyway, which is
// what a store wants for something it drop-ships, back-orders or prints on
// demand.
//
// It is the reason the two stock CHECKs are rewritten rather than dropped. An
// oversold variant is exactly a variant whose reserved has passed its on-hand,
// so the invariant cannot hold unconditionally — but it must still hold for
// every variant that has not opted out, because that is the guarantee the
// reservation path is built on. Conditioning them on the flag keeps the
// database the thing that enforces it, rather than the service remembering to.
const migration0012ContinueSelling = `
ALTER TABLE variants
    ADD COLUMN continue_selling boolean NOT NULL DEFAULT false;

ALTER TABLE variants
    DROP CONSTRAINT variants_reserved_within_on_hand,
    DROP CONSTRAINT variants_stock_on_hand_check,
    ADD CONSTRAINT variants_reserved_within_on_hand
        CHECK (continue_selling OR stock_reserved <= stock_on_hand),
    ADD CONSTRAINT variants_stock_on_hand_check
        CHECK (continue_selling OR stock_on_hand >= 0);
`

// M13 — the choices a taxonomy attribute offers.
//
// A category says which fields it asks of a product, in its own metadata. What
// it does not say is what may be answered: "Color" offers nineteen values and
// they are the same nineteen wherever Color is asked. Written on every category
// that uses it, Shopify's set costs 29MB and puts 400KB of repeated text into
// every page of a category listing; written once, it is 1.5MB and the listing
// carries none of it.
//
// Keyed by handle rather than by an id of our own. The handle is what the
// category's metadata names, it is stable across taxonomy releases, and a store
// that writes its own fields can use whatever handle it likes without asking
// for a row here first — an attribute with no entry simply offers no fixed
// choices, which is a free-text field.
const migration0013TaxonomyAttributes = `
CREATE TABLE taxonomy_attributes (
    handle     text PRIMARY KEY CHECK (handle <> ''),
    label      text NOT NULL CHECK (label <> ''),
    choices    text[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now()
);
`

// M14 — which carrier is actually carrying it.
//
// `provider` already says how the shipment was booked — by hand, or through an
// integration — and that is not the same fact as who is driving the van. A
// store that packs its own boxes books every shipment as "manual" and hands
// them to a different courier depending on the pincode.
//
// It is what turns a tracking number into a link, so it is a column rather than
// a note in metadata: a customer following their parcel is the whole reason the
// number is recorded.
const migration0014FulfillmentCarrier = `
ALTER TABLE fulfillments
    ADD COLUMN carrier text NOT NULL DEFAULT '';
`

// M13 — roles.
//
// Every operator that exists today was the only kind there was: someone with a
// password who could do anything. So the column defaults to owner, and an
// upgrade changes nobody's access. The CHECK is the engine's own list of roles
// (rights.go) written where the database can enforce it — a role it has never
// heard of should not be storable, since an unknown role resolves to nothing
// and a row like that would lock somebody out with no way to see why.
const migration0013Roles = `
ALTER TABLE superusers
    ADD COLUMN role text NOT NULL DEFAULT 'owner'
        CHECK (role IN ('owner', 'manager', 'staff'));
`

// M15 — discounts.
//
// `orders.discount_minor` has existed since M3 and has been written as zero
// ever since: the column was left for this. So this migration adds the rule,
// not the effect — what an order was given is already expressible.
//
// Three tables, because a discount is three different things. `discounts` is a
// rule an operator maintains. `discount_targets` is what a scoped rule points
// at. `order_discounts` is the snapshot, and it exists for the same reason
// `order_lines` snapshots a price rather than pointing at a variant: a finished
// promotion may be deleted, and an order from last winter must still say what
// it was given and what it was called.
const migration0015Discounts = `
CREATE TABLE discounts (
    id            bigserial   PRIMARY KEY,
    -- NULL means automatic: it applies without anybody typing it. Nothing
    -- evaluates those yet (D29); the column is what keeps that additive.
    code          text        CHECK (code IS NULL OR code <> ''),
    title         text        NOT NULL CHECK (title <> ''),
    kind          text        NOT NULL
                              CHECK (kind IN ('percentage', 'fixed', 'free_shipping')),
    -- Basis points: 1000 is 10.00%. An integer, because a percentage of money
    -- is money-adjacent and floats are forbidden anywhere near it.
    value_bp      integer     CHECK (value_bp IS NULL OR (value_bp > 0 AND value_bp <= 10000)),
    value_minor   bigint      CHECK (value_minor IS NULL OR value_minor > 0),
    CONSTRAINT discounts_value_matches_kind CHECK (
        (kind = 'percentage'    AND value_bp IS NOT NULL AND value_minor IS NULL) OR
        (kind = 'fixed'         AND value_minor IS NOT NULL AND value_bp IS NULL) OR
        (kind = 'free_shipping' AND value_bp IS NULL AND value_minor IS NULL)
    ),

    scope              text   NOT NULL DEFAULT 'order'
                              CHECK (scope IN ('order', 'products', 'collections', 'categories')),
    min_subtotal_minor bigint CHECK (min_subtotal_minor IS NULL OR min_subtotal_minor >= 0),

    starts_at     timestamptz,
    ends_at       timestamptz,
    CONSTRAINT discounts_window CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at),

    usage_limit   integer     CHECK (usage_limit IS NULL OR usage_limit > 0),
    used_count    integer     NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    -- Per email, not per customer, and named so. Guest checkout is permanent
    -- (D22), so there is no customer to count against and this is a deterrent
    -- rather than a control. The schema says which.
    once_per_email boolean    NOT NULL DEFAULT false,

    active        boolean     NOT NULL DEFAULT true,
    metadata      jsonb       NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Unique and matched case-insensitively: customers type codes off posters. The
-- folding is in SQL and only in SQL — PostgreSQL's lower() follows the database
-- collation and Go's strings.ToLower does not agree with it on accented
-- characters, and one implementation of case folding is the only version of
-- this that stays true. See the same lesson in taxonomy.go.
CREATE UNIQUE INDEX discounts_code_key ON discounts (lower(code)) WHERE code IS NOT NULL;
CREATE INDEX discounts_active_idx ON discounts (active, id DESC);

CREATE TABLE discount_targets (
    discount_id bigint NOT NULL REFERENCES discounts (id) ON DELETE CASCADE,
    kind        text   NOT NULL CHECK (kind IN ('product', 'collection', 'category')),
    target_id   bigint NOT NULL,
    PRIMARY KEY (discount_id, kind, target_id)
);

CREATE TABLE order_discounts (
    id           bigserial PRIMARY KEY,
    order_id     bigint    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    -- SET NULL rather than RESTRICT: deleting a promotion that ended is a
    -- normal thing to do, and the order keeps its own copy of what it got.
    discount_id  bigint    REFERENCES discounts (id) ON DELETE SET NULL,
    code         text      NOT NULL DEFAULT '',
    title        text      NOT NULL,
    kind         text      NOT NULL,
    amount_minor bigint    NOT NULL CHECK (amount_minor >= 0),
    -- One per order today (D28). The key allows several so that stacking is a
    -- service change later rather than a migration.
    UNIQUE (order_id, title)
);
CREATE INDEX order_discounts_discount_idx ON order_discounts (discount_id);

-- The cart holds what somebody typed. Whether it is real is decided at
-- checkout, under the same lock that re-checks price and stock.
ALTER TABLE carts ADD COLUMN discount_code text NOT NULL DEFAULT '';
`

// M16 — tax.
//
// A rate is a rule about a place and a kind of thing: 18% on electronics sold
// into Karnataka. So a rate carries a country, optionally a state, and
// optionally a category — and the most specific rule that fits a line is the one
// that applies. A rate on "Apparel" reaches "Apparel / Shirts" beneath it,
// because that is what a category tree is for.
//
// What each line was charged is stored on the line. An invoice has to show tax
// per line, and recomputing it later from a rate that has since changed would
// print a different invoice for the same order — which is the one thing an
// invoice may never do.
//
// `orders.tax_inclusive` snapshots which way the store was working when the
// order was placed. Switching a live store from exclusive to inclusive pricing
// changes what every future total means, and every past order has to keep
// meaning what it meant.
const migration0016Taxes = `
CREATE TABLE tax_rates (
    id          bigserial   PRIMARY KEY,
    name        text        NOT NULL CHECK (name <> ''),
    -- Basis points, like a discount: 1800 is 18.00%. Integers all the way down.
    rate_bp     integer     NOT NULL CHECK (rate_bp >= 0 AND rate_bp <= 10000),

    -- Where it applies. An empty country is the fallback every other rule is
    -- more specific than, which is how a single-jurisdiction store configures
    -- one rate and stops thinking about it.
    country     text        NOT NULL DEFAULT '',
    state       text        NOT NULL DEFAULT '',
    -- What it applies to. NULL is everything; a category reaches its whole
    -- subtree. RESTRICT, because deleting a category out from under a live tax
    -- rule should be refused rather than silently widening the rule.
    category_id bigint      REFERENCES categories (id) ON DELETE RESTRICT,

    active      boolean     NOT NULL DEFAULT true,
    metadata    jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    -- One rule per (place, thing). Two rates for the same pair is not a
    -- preference to resolve at checkout, it is a mistake to refuse at entry.
    CONSTRAINT tax_rates_unique_target
        UNIQUE NULLS NOT DISTINCT (country, state, category_id)
);
CREATE INDEX tax_rates_lookup_idx ON tax_rates (active, country, state);

-- What each line was actually charged, snapshotted like its price.
ALTER TABLE order_lines
    ADD COLUMN tax_minor   bigint  NOT NULL DEFAULT 0 CHECK (tax_minor >= 0),
    ADD COLUMN tax_rate_bp integer NOT NULL DEFAULT 0
               CHECK (tax_rate_bp >= 0 AND tax_rate_bp <= 10000),
    ADD COLUMN tax_name    text    NOT NULL DEFAULT '';

ALTER TABLE orders
    ADD COLUMN tax_minor     bigint  NOT NULL DEFAULT 0 CHECK (tax_minor >= 0),
    -- Whether the prices on this order already contained the tax. Snapshotted,
    -- because it decides what every figure on the order means.
    ADD COLUMN tax_inclusive boolean NOT NULL DEFAULT false;
`

// M17 — stock lives in a place.
//
// Until now a variant had one number and it was implicitly the whole business:
// `variants.stock_on_hand`. A store with a shop and a warehouse could not say
// which units were where, and every question that matters — can I ship this
// today, which box does this order come out of, what did I actually count last
// Tuesday — needs that answer.
//
// The per-location rows become the only truth and the variant columns go. The
// API keeps `stock_on_hand`, `stock_reserved` and `available` exactly as they
// were, because clients and the MCP module read them; they are now sums across
// locations, computed on the way out. A stored copy of a sum is a number that
// can be wrong, and this codebase has said so before about category paths.
//
// One thing genuinely moves from the database to the service. M12's CHECK —
// reserved may not pass on-hand unless the variant sells past zero — cannot be
// written on a row that does not carry `continue_selling`, and copying the flag
// onto every stock row would be the same stored-duplicate mistake. What replaces
// it is the guard that was always doing the real work: reserveStock's
// conditional UPDATE, which refuses in the same statement that decrements, so
// there is no window between the check and the change. `doctor` verifies the
// invariant across every row, which is where a drift would surface.
const migration0017Locations = `
CREATE TABLE locations (
    id         bigserial   PRIMARY KEY,
    code       text        NOT NULL UNIQUE CHECK (code <> ''),
    name       text        NOT NULL CHECK (name <> ''),
    address    jsonb       NOT NULL DEFAULT '{}',
    -- Lower is preferred. Where a reservation may come from more than one
    -- place, it is taken from the first that can cover it.
    priority   integer     NOT NULL DEFAULT 0,
    active     boolean     NOT NULL DEFAULT true,
    is_default boolean     NOT NULL DEFAULT false,
    metadata   jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Exactly one default, enforced rather than remembered: "which location is this
-- going out of" must always have an answer, and two answers is worse than none.
CREATE UNIQUE INDEX locations_one_default ON locations ((is_default)) WHERE is_default;

CREATE TABLE variant_stock (
    variant_id  bigint      NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    -- RESTRICT: closing a location that still holds stock is a decision about
    -- the stock, and it has to be made before the location can go.
    location_id bigint      NOT NULL REFERENCES locations (id) ON DELETE RESTRICT,
    on_hand     integer     NOT NULL DEFAULT 0,
    reserved    integer     NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (variant_id, location_id)
);
CREATE INDEX variant_stock_location_idx ON variant_stock (location_id);

-- Everything that exists today is in one place, and this is that place. The
-- name is deliberately plain: a store with one location should never have to
-- think about locations at all.
INSERT INTO locations (code, name, is_default, priority)
VALUES ('default', 'Main location', true, 0);

INSERT INTO variant_stock (variant_id, location_id, on_hand, reserved)
SELECT v.id, l.id, v.stock_on_hand, v.stock_reserved
FROM variants v CROSS JOIN locations l
WHERE l.is_default;

-- Where a line's units came from, so cancelling puts them back where they were
-- rather than wherever the default happens to be today. SET NULL because an
-- order is a snapshot and must stay readable after a location is closed.
ALTER TABLE order_lines
    ADD COLUMN location_id bigint REFERENCES locations (id) ON DELETE SET NULL;
UPDATE order_lines SET location_id = (SELECT id FROM locations WHERE is_default);

-- The columns go last, so the backfill above reads them.
ALTER TABLE variants
    DROP COLUMN stock_on_hand,
    DROP COLUMN stock_reserved;
`

// M18 — inviting somebody instead of inventing their password.
//
// Until now the only way onto a team was for an owner to type a password on
// somebody else's behalf and then tell it to them: over chat, or out loud. That
// password is known to two people from the moment it exists, it usually never
// gets changed, and the store has no way to tell whether the person on the other
// end ever received it. An invitation replaces all of that — the invitee sets a
// password nobody else has seen, and the store can see who has and has not
// joined.
//
// Only the hash of the token is stored, exactly as with superuser_sessions: a
// leaked database yields no usable invitation, and the token exists solely in
// the link the invitee was sent.
const migration0018Invitations = `
CREATE TABLE superuser_invitations (
    id          bigserial   PRIMARY KEY,
    -- Normalised the same way superusers.email is, so the open-invitation index
    -- below actually catches a second invite to the same person.
    email       text        NOT NULL CHECK (email <> ''),
    role        text        NOT NULL CHECK (role <> ''),
    token_hash  text        NOT NULL UNIQUE,
    -- SET NULL rather than CASCADE: who invited whom is a fact about the past,
    -- and it should survive that person leaving. The invitation is still valid.
    invited_by  bigint      REFERENCES superusers (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    -- NULL while outstanding. Kept rather than deleted on acceptance, so the
    -- team screen can answer "who let this person in, and when".
    accepted_at timestamptz
);

-- One outstanding invitation per address. A second one would leave two live
-- links for the same person, and revoking the one the owner can see would not
-- close the door.
CREATE UNIQUE INDEX superuser_invitations_open
    ON superuser_invitations (email) WHERE accepted_at IS NULL;
CREATE INDEX superuser_invitations_expiry_idx ON superuser_invitations (expires_at);
`

// M19 — what a role may do becomes the store's decision.
//
// Until now roleRights in rights.go was the whole permission model, and a store
// that wanted its managers kept away from refunds had to fork the engine. This
// table holds the store's departures from those defaults; a role with no rows
// here is a role still tracking the built-in set, which is what makes a later
// change to the defaults reach the stores that never touched them.
//
// Only the roles that can be re-cut are storable. Owner is deliberately absent
// from the CHECK: it is the recovery path, and the way back into a store that
// has been configured into a corner must not itself depend on configuration
// being sane.
//
// right_name carries no foreign key, because the rights live in Go and not in a
// table. A right this engine no longer has leaves a row nothing reads — the
// lookup intersects with AllRights — and that is the safe direction to be
// wrong in.
const migration0019RoleRights = `
CREATE TABLE role_rights (
    role       text        NOT NULL CHECK (role IN ('manager', 'staff')),
    right_name text        NOT NULL CHECK (right_name <> ''),
    granted_at timestamptz NOT NULL DEFAULT now(),
    -- SET NULL rather than CASCADE: who widened a role is a fact about the
    -- past, and it outlives their account. The grant itself still stands.
    granted_by bigint      REFERENCES superusers (id) ON DELETE SET NULL,
    PRIMARY KEY (role, right_name)
);
`

// M20 — who did it.
//
// Twenty rights decided who may act (M19) and the store recorded who let
// somebody in (M18), and then nothing recorded what any of them actually did.
// The outbox is not that record: it announces order state to consumers, carries
// no actor, has one aggregate type, and its rows are a delivery queue rather
// than history. This is the other half — written in the same transaction as the
// change, read only by operators, and delivered nowhere.
//
// It carries no CHECK, no foreign key and no unique index but the primary key.
// That is the design, not an omission: this row is written inside somebody
// else's transaction, so every constraint on it is a way for the record of a
// change to veto the change — and a failed statement poisons a transaction, so
// there is no ignoring one once it has fired. The vocabulary lives in Go
// (audit.go), which is where it can be enforced without a rollback: the same
// reasoning M19 gives for role_rights.right_name carrying no foreign key.
//
// actor_email and actor_role are snapshots rather than joins, for the reason
// M18 gives for keeping superuser_invitations.invited_by: who did it is a fact
// about the past and has to survive that person leaving. A dangling actor_id is
// the safe direction to be wrong in.
const migration0020AdminAudit = `
CREATE TABLE admin_audit (
    id           bigserial   PRIMARY KEY,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Three kinds of caller, and they are not the same fact: a person, a script
    -- holding a static admin token, and the engine's own background work.
    -- 'system' is the default because that is honestly what a row written by a
    -- caller nobody identified is.
    actor_kind   text        NOT NULL DEFAULT 'system',
    actor_id     bigint,
    actor_email  text        NOT NULL DEFAULT '',
    -- The role as it was at the time. Somebody who is a manager today may have
    -- been staff when they did this, and the row has to say which.
    actor_role   text        NOT NULL DEFAULT '',
    -- A free description for a caller that is not a person: the sweeper's name,
    -- a cron job, a module naming itself. Never the token.
    actor_label  text        NOT NULL DEFAULT '',
    action       text        NOT NULL,
    entity_type  text        NOT NULL,
    -- text rather than bigint, because not everything an operator changes is
    -- numbered. A role is 'manager', and a column that could not hold it would
    -- push the one change a store most wants attributed into a special case.
    entity_id    text        NOT NULL,
    -- What the record was called at the time: an order number, a SKU, a title.
    -- Kept so the feed stays readable after the record is renamed, and after it
    -- is deleted — a deleted product is exactly what somebody opens the log to
    -- ask about.
    entity_label text        NOT NULL DEFAULT '',
    summary      text        NOT NULL DEFAULT '',
    -- {"before": {...}, "after": {...}, "event": "order.cancelled"}. A key in
    -- after with none in before means the previous value was not recorded,
    -- never that it was empty. Money keys are the same *_minor integers the API
    -- uses; nothing in here is a formatted string. "event" names the outbox
    -- event this same transaction published, which is what lets an order
    -- timeline drawn from both sources render each fact once.
    changes      jsonb       NOT NULL DEFAULT '{}'
);

-- One record's history: what the per-record History card asks for, and the only
-- query on this table that runs on a screen an operator opens all day.
CREATE INDEX admin_audit_entity_idx ON admin_audit (entity_type, entity_id, id DESC);

-- "What has this person done." Partial, because in a store that runs scripts
-- the rows with no person behind them are the majority and are never the answer
-- to that question.
CREATE INDEX admin_audit_actor_idx ON admin_audit (actor_id, id DESC)
    WHERE actor_id IS NOT NULL;

-- There is no third index. Two entries per audited write is already the cost
-- this table adds to every order transition; the unfiltered, action-filtered and
-- date-ranged reads walk the primary key backwards, which is time order, and
-- created_at is now() — transaction_timestamp() — so two rows can share an
-- instant and id is the tie-break. The feed is ordered by id, never by
-- created_at.
`

// M21 — a way back in that does not go through another operator.
//
// Until now an operator who forgot their password had two remedies and both
// went through somebody else: an owner typing a password on their behalf in the
// Edit drawer — the exact practice M18's invitations exist to end — or shell
// access to `gocommerce superuser update`. A single-owner store whose owner
// forgot their password was locked out entirely.
//
// Only the SHA-256 of the token is stored, exactly as with superuser_sessions
// and superuser_invitations: a leaked database yields no usable link, and the
// token exists solely in the email that was sent.
//
// token_hash is the key rather than superuser_id, deliberately. Keying on the
// operator would make "one live link" a schema fact, but it would also make
// asking again destroy the link already sitting in the victim's mailbox — and
// unlike re-inviting, which only an authenticated owner can trigger, this
// request is public. Several live links are therefore allowed, and ConfirmReset
// deletes every one of that operator's rows when it spends one: the safety
// without the denial of service.
const migration0021PasswordResets = `
CREATE TABLE superuser_password_resets (
    token_hash   text        PRIMARY KEY,
    -- CASCADE, where superuser_invitations.invited_by is SET NULL. An
    -- invitation records a fact about the past and should outlive the inviter;
    -- an outstanding reset for an operator who no longer exists is not history,
    -- it is a live key to an account that is gone.
    superuser_id bigint      NOT NULL REFERENCES superusers (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    -- A row born expired would present as "the link did not work and nobody can
    -- say why".
    CHECK (expires_at > created_at)
);

-- Spending one link drops every other link that operator holds, and any password
-- or email change drops them all; every one of those deletes is by superuser_id.
CREATE INDEX superuser_password_resets_user_idx
    ON superuser_password_resets (superuser_id);

-- The opportunistic sweep, as superuser_invitations_expiry_idx serves for
-- invitations. Expiry itself is enforced by the expires_at > now() predicate in
-- ConfirmReset, so a store that never sweeps stays correct and merely untidy.
CREATE INDEX superuser_password_resets_expiry_idx
    ON superuser_password_resets (expires_at);
`

// M22 — half an order can go out, and the order can say so.
//
// `fulfillments` has recorded a parcel since M4 but never what was in it,
// because there was only one answer it could have: Create shipped the whole
// order and shippableOrder refused a second one. A line table is what makes a
// second parcel expressible, and `partial` is what makes the order readable
// while it is true — an operator holding a back-ordered line had to sit on the
// whole order or mark all of it shipped, and the second is a lie the customer
// finds out about before the shop does. PLAN §10.1 drew a state in this slot
// and left it unnamed; this is the concrete answer to what it was for.
//
// The quantities are a table rather than a note in `fulfillments.metadata`
// because the remainder is recomputed under the order's row lock on every
// shipment, and an aggregate over rows is the only form of that question the
// database can answer. Same reasoning M14 used for the carrier column.
const migration0022PartialFulfillment = `
CREATE TABLE fulfillment_lines (
    fulfillment_id bigint  NOT NULL REFERENCES fulfillments (id) ON DELETE CASCADE,
    -- CASCADE on both, and deliberately not RESTRICT: an order line can only
    -- disappear with its order, because EditLines refuses on a partly shipped
    -- one. RESTRICT is checked immediately, and orders cascades into
    -- order_lines and into fulfillments down two independent chains, so it
    -- would make deleting an order fail on the ordering of its own cascade.
    order_line_id  bigint  NOT NULL REFERENCES order_lines (id) ON DELETE CASCADE,
    quantity       integer NOT NULL CHECK (quantity > 0),
    -- One row per line per parcel. Shipping the same line twice is two
    -- fulfillments, which is the whole point; twice in one parcel is a client
    -- that built its request wrong.
    PRIMARY KEY (fulfillment_id, order_line_id)
);
-- The aggregate that decides the order's status starts from the order line,
-- which the primary key's leading column does not serve.
CREATE INDEX fulfillment_lines_order_line_idx ON fulfillment_lines (order_line_id);

-- Every shipment recorded before now covered the whole order, so writing that
-- down costs nothing and keeps "what has shipped" a single sum.
--
-- DISTINCT ON because one live shipment per order was all the old
-- shippableOrder allowed. A second non-cancelled row on the same order was not
-- reachable through the engine; crediting it with the whole order as well would
-- make the sum say twice what was ordered, so it gets no lines and reads as an
-- empty parcel — the safe direction to be wrong in. A cancelled fulfillment
-- gets none either: its units are owed again, which is exactly what the
-- derivation already excludes it for.
INSERT INTO fulfillment_lines (fulfillment_id, order_line_id, quantity)
SELECT f.id, ol.id, ol.quantity
FROM (
    SELECT DISTINCT ON (order_id) id, order_id
    FROM fulfillments
    WHERE status <> 'cancelled'
    ORDER BY order_id, id
) f
JOIN order_lines ol ON ol.order_id = f.order_id;

-- partial is dropped in beside the others rather than replacing anything:
-- shipped still means all of it has gone, which is what every existing client,
-- filter and report already assumes.
--
-- Dropped by the name PostgreSQL generated for M3's inline column CHECK, and
-- without IF EXISTS. A database whose constraint is called something else must
-- fail here, loudly, rather than silently keep the old one and reject the first
-- partial shipment months later. This is the move M12 already made on the two
-- stock CHECKs.
ALTER TABLE orders
    DROP CONSTRAINT orders_status_check,
    ADD CONSTRAINT orders_status_check
        CHECK (status IN ('pending', 'confirmed', 'partial', 'shipped',
                          'delivered', 'cancelled'));
`

// M23 — the sweeper's index follows the sweeper.
//
// orders_unpaid_idx (M3) was partial on (status = 'pending' AND payment_status
// = 'pending'), which was exactly the set SweepUnpaid scanned. An operator can
// now record a failed payment from the panel, and a payment recorded as failed
// is the one nobody is coming back for, so the sweep and the doctor's
// stale-reservation check both widen to payment_status IN ('pending','failed').
// That predicate does not imply the old one, so the old index cannot serve the
// new query.
//
// Both widened queries spell their constants inline rather than binding them. A
// partial index is used only when the planner can prove the query's WHERE
// implies the index predicate, and the sweep runs every five minutes through a
// cached statement, so a generic plan over $1/$2 would prove nothing about
// 'pending' and this index would be ignored — a seq-scan of orders on every
// pass, which is the outcome the index exists to prevent.
//
// The old index is dropped rather than kept beside it: the new predicate is a
// strict superset and answers every query the old one did, so keeping both
// would cost a write on every order insert to answer nothing. Dropping an
// object in a LATER migration is not a breach of append-only — shipped SQL is
// frozen and this edits none of it — and it is precedented here, in M12 and
// M22.
const migration0023UnsettledSweep = `
CREATE INDEX orders_unsettled_idx ON orders (reservation_expires_at)
    WHERE status = 'pending' AND payment_status IN ('pending', 'failed');

DROP INDEX IF EXISTS orders_unpaid_idx;
`

// M24 — what a refund actually was.
//
// Until now a refund wrote one word: payment_status = 'refunded', whatever the
// amount. A store that sent back 200 of a 1000 order recorded that it had sent
// back all of it, could not send the rest — the not-paid guard then refused
// everything — and told the customer nothing, because the transition returned
// no event at all. Three things fix it: a ledger of what went back, a running
// total on the order, and a CHECK that keeps the two inside the order.
//
// A table rather than only a column, because a refund is an event with its own
// facts — how much, why, through which gateway, whose decision, when — and a
// column holds none of them. A column as well as the table, which is where this
// departs from M17 (the migration that dropped variants.stock_on_hand precisely
// so a stored sum could not be wrong) and says so: the stored total is what
// makes orders_refunded_within_total a real row CHECK, and what lets lockOrder
// read the figure off the row it already holds FOR UPDATE instead of off a
// snapshot that can be one commit behind under READ COMMITTED. `doctor`
// reconciles the two on every run, which is the price of the departure.
//
// The status column exists because the provider call cannot happen inside a
// transaction (AGENTS rule 5). A 'pending' row is committed first and is what
// reserves the amount against a second refund while the first is in flight;
// only 'succeeded' is money that moved.
//
// payment_status keeps its four values (D36): 'paid' while the store still
// holds any of the money, 'refunded' once the running total reaches
// orders.total_minor. A fifth value would be two money bugs rather than eight
// cosmetic edits — ext/fulfill-shiprocket books Prepaid only on exactly 'paid'
// and would have the courier collect the whole total again, and ext/invoices
// selects WHERE payment_status = 'paid' and would silently stop invoicing.
const migration0024OrderRefunds = `
CREATE TABLE order_refunds (
    id           bigserial   PRIMARY KEY,
    order_id     bigint      NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    -- Strictly positive. A refund of nothing is not a fact about money, it is a
    -- mistyped one, and Refund reads a non-positive amount as "the rest". The
    -- upper bound spans rows and cannot be a row CHECK; it is held by the
    -- reservation in Payments.reserveRefund and backstopped by
    -- orders_refunded_within_total below.
    amount_minor bigint      NOT NULL CHECK (amount_minor > 0),
    reason       text        NOT NULL DEFAULT '',
    -- Snapshotted rather than read off the order, for order_lines' reason: the
    -- money went out through the method that was on the order at the time, and
    -- this row must still say which one next year. Orders.Update can still
    -- correct payment_provider while nothing has been refunded.
    provider     text        NOT NULL,
    -- The gateway's own id for the refund: what somebody reconciles against a
    -- bank statement, which is the job orders.payment_reference does for the
    -- charge. Empty when the provider does not implement ReferencedRefunder.
    provider_reference text  NOT NULL DEFAULT '',
    -- pending only while the provider is being asked. The row is committed
    -- before that call and is what stops a second refund spending the same
    -- money while the first is in flight.
    status       text        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'succeeded', 'failed')),
    -- What the provider said when it refused. Kept because an operator looking
    -- at a refund that did not happen needs to know it was tried, and why.
    error        text        NOT NULL DEFAULT '',
    -- SET NULL rather than CASCADE, like superuser_invitations.invited_by: who
    -- authorised a refund is a fact about the past and outlives their account,
    -- so the id may go and the actor column — their email as it was then — stays. Both
    -- empty for the static admin token, which is a credential and not a person.
    superuser_id bigint      REFERENCES superusers (id) ON DELETE SET NULL,
    actor        text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX order_refunds_order_idx ON order_refunds (order_id, id);
-- The doctor's in-flight query and nothing else, so it stays tiny.
CREATE INDEX order_refunds_pending_idx ON order_refunds (created_at)
    WHERE status = 'pending';

ALTER TABLE orders
    ADD COLUMN refunded_minor bigint NOT NULL DEFAULT 0;

-- Every order that already says 'refunded' had its whole total sent back,
-- because that is the only thing the old code could record. Writing those as
-- rows makes the ledger the single answer to "how much came back" for history
-- as well, which is what lets the doctor check be unconditional rather than
-- conditional-and-correct-by-accident. The amount, the provider and the
-- timestamp are real; the reason says where the figure came from, so a wrong
-- one can be found later. total_minor > 0 because a 100%-discount order would
-- otherwise violate amount_minor > 0.
INSERT INTO order_refunds (order_id, amount_minor, reason, provider, status, created_at, updated_at)
SELECT id, total_minor, 'refunded before this store recorded refunds one at a time',
       payment_provider, 'succeeded', updated_at, updated_at
FROM orders
WHERE payment_status = 'refunded' AND total_minor > 0;

UPDATE orders SET refunded_minor = total_minor WHERE payment_status = 'refunded';

-- The constraint goes last, so the backfill above runs under no constraint and
-- the validation pass sees rows that already satisfy it.
ALTER TABLE orders
    ADD CONSTRAINT orders_refunded_within_total
        CHECK (refunded_minor >= 0 AND refunded_minor <= total_minor);
`

// No currency column on order_refunds: one settlement currency per store (D14),
// the order already snapshots it, and a second copy could only ever disagree.

// M25 — goods coming back.
//
// The engine has twice named this operation in refusal messages without having
// it: Cancel says "cancelling it is a return, not a cancellation" and EditLines
// says "changing what is in it is a return, not an edit". Until now the only
// way to put returned goods back was to walk a delivered order backwards —
// undeliver, delete the shipment, cancel — which restocks every line at its
// full quantity and rewrites a completed sale as a cancellation.
//
// Two tables rather than a `restock` flag on the refund route, for three
// reasons. Refund requires payment_status = 'paid' and a provider implementing
// Refunder, and cash on delivery — the only method core ships — is neither, so
// a flag there could not restock a single return in the engine's own default
// store. What makes a return safe is memory: without a record of what has
// already come back, a second request for the same line restocks the same units
// again. And refunding is a network call that cannot happen inside the
// transaction that decides whether the return is even legal (rule 5).
//
// The quantity and the restock decision are per line because a parcel comes
// back with three items and one of them is broken: that is the ordinary case,
// and an order-level flag forces a manual adjustment for the remainder — the
// anonymous stock correction this whole feature exists to abolish.
//
// No money column here beyond the per-line snapshot below: what was refunded is
// a separate fact, with a separate operation and its own record (M24).
const migration0025Returns = `
CREATE TABLE order_returns (
    id       bigserial PRIMARY KEY,
    order_id bigint    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    -- Why it came back, in the store's own words. Deliberately not a CHECK
    -- list: a constraint here would mean a migration every time a store
    -- thought of a new reason.
    reason   text      NOT NULL DEFAULT '',
    -- Two states, not a workflow. 'received' is the fact this table exists to
    -- record; 'withdrawn' is that fact taken back. Withdrawing has to be a
    -- state rather than a delete because how much is still returnable is
    -- counted from these rows, and a return whose row is gone is a stock
    -- movement nobody can account for.
    status   text      NOT NULL DEFAULT 'received'
                       CHECK (status IN ('received', 'withdrawn')),
    metadata jsonb     NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX order_returns_order_idx ON order_returns (order_id, id);

CREATE TABLE order_return_lines (
    id            bigserial PRIMARY KEY,
    return_id     bigint    NOT NULL REFERENCES order_returns (id) ON DELETE CASCADE,
    -- NOT NULL, and CASCADE like every other child of an order. Nothing can
    -- reach a line an active return points at: EditLines refuses on a shipped
    -- order and now on any order with an active return, and no route deletes
    -- an order. So this fires only when the whole order goes.
    order_line_id bigint    NOT NULL REFERENCES order_lines (id) ON DELETE CASCADE,
    quantity      integer   NOT NULL CHECK (quantity > 0),
    -- Whether these units went back on sale. False is a real answer: a damaged
    -- item comes back and is still returned, it simply moves no stock. It
    -- records what happened rather than what was asked, so a line whose variant
    -- has since been deleted stores false — the snapshot is still readable
    -- history, there is just no shelf left to put it back on.
    restocked     boolean   NOT NULL,
    -- Which shelf they went on: the line's own by default, so a return lands
    -- where the sale came from. NULL when nothing moved. SET NULL, with no
    -- CHECK tying it to ` + "`restocked`" + `: locations can be deleted once empty, that
    -- deletion fires this SET NULL as an UPDATE, and a CHECK would reject it
    -- and leave an undeletable location behind a raw constraint error.
    location_id   bigint    REFERENCES locations (id) ON DELETE SET NULL,
    -- What the customer paid for these units: unit price x quantity, less this
    -- line's share of the order-level discount, plus its share of the line's
    -- tax when prices are exclusive — the same arithmetic checkout used to
    -- reach the order total. Snapshotted rather than derived on read because
    -- the discount behind it is editable, and a figure recomputed next year
    -- against today's rules would not be what happened. Both shares are
    -- apportioned by quantity and round down, so they are exact when a whole
    -- line comes back. It is what the goods were worth, not a refund: the
    -- money is a separate operation.
    refundable_minor bigint NOT NULL CHECK (refundable_minor >= 0),
    -- One entry per line per return. Naming a line twice in one request has
    -- two possible meanings; the service refuses it, and this is the same rule
    -- where it cannot be bypassed.
    UNIQUE (return_id, order_line_id)
);
-- The cap reads every return line for one order line, on every return. It
-- rides an index rather than the table.
CREATE INDEX order_return_lines_line_idx ON order_return_lines (order_line_id, return_id);
`

// M26 — the stock ledger.
//
// Until now every stock movement was an in-place UPDATE and the only trace was
// updated_at, which the next movement overwrote. "The count says 4 and the
// shelf has 2" had no answer anywhere in the system and a stock-take dispute
// could not be settled. Orders have kept a durable history since M3
// (outbox_events); stock kept none.
//
// Two deltas rather than one signed number, because variant_stock holds two
// independent counters and commitStock moves both in one statement: a
// reservation leaving the shelf is not the same event as a unit leaving it, and
// one column cannot say which happened. The balances *after* sit beside them so
// a row reads on its own, without summing every row before it — which is what a
// paged list starting in the middle of history requires, and what lets `doctor`
// find a drift with one query.
//
// Every row records what the statement APPLIED, never what the caller asked
// for. Five of the writers wrap their quantity in `CASE WHEN track_inventory`,
// one floors a release at zero, and two write absolutely — so
// `sum(on_hand_delta)` reconciling to `on_hand` for every pair is a property of
// the writers, not of the schema, and it is the property the whole table exists
// for.
//
// The names are snapshots, for the reason order_lines snapshots sku and title:
// history that dissolves when a SKU is deleted, a location closes or an
// employee leaves is exactly the history a dispute needs. It also makes both
// read routes a single-table index scan with no joins.
const migration0026StockMovements = `
CREATE TABLE stock_movements (
    id             bigserial   PRIMARY KEY,
    -- SET NULL rather than variant_stock's CASCADE (M17). That CASCADE is right
    -- for a live balance and wrong for history: variants really are deleted
    -- (catalog.go, options.go), and "who deleted the SKU that held 40 units" is
    -- a question this table exists to answer. The snapshot beside it is what
    -- keeps the row legible afterwards — and it is a snapshot, so a SKU renamed
    -- later leaves older rows naming what the shelf was called at the time.
    variant_id     bigint      REFERENCES variants (id) ON DELETE SET NULL,
    sku            text        NOT NULL,
    -- SET NULL rather than variant_stock's RESTRICT. That RESTRICT stops a
    -- location leaving while it holds units, and refuseIfHolding already
    -- enforces it before the delete runs; a ledger that also refused would make
    -- every location ever counted permanently undeletable, which is a different
    -- policy than this table was asked for. order_lines.location_id is the
    -- precedent, and its reason is this one verbatim.
    location_id    bigint      REFERENCES locations (id) ON DELETE SET NULL,
    location_code  text        NOT NULL,
    kind           text        NOT NULL CHECK (kind IN (
                       'opening', 'adjust', 'stock_take', 'import',
                       'transfer_out', 'transfer_in',
                       'reserve', 'commit', 'release', 'restock', 'sell')),
    -- Which PATH moved the stock, not who: whether a person or the static admin
    -- token was behind an 'admin' movement is superuser_id's job, and storing
    -- that twice would be a stored duplicate. Paired with a nullable
    -- superuser_id this makes a null actor a fact rather than a hole —
    -- 'checkout' and 'sweeper' have no person by definition.
    source         text        NOT NULL CHECK (source IN (
                       'admin', 'checkout', 'order', 'sweeper', 'import', 'migration')),
    on_hand_delta  integer     NOT NULL,
    reserved_delta integer     NOT NULL,
    -- No CHECK on either balance. on_hand legitimately goes negative for a
    -- variant with continue_selling, and a ledger constraint stricter than
    -- variant_stock's own (which has only CHECK (reserved >= 0)) would refuse to
    -- record a sale the shelf accepted. An audit trail must never become a
    -- business rule.
    on_hand_after  integer     NOT NULL,
    reserved_after integer     NOT NULL,
    reason         text        NOT NULL DEFAULT '',
    -- The other end of a transfer. "3 units left here" is half an answer without
    -- "and went to the shop", and the pair cannot be found by timestamp because
    -- both rows are written by one transaction and share now() exactly. The id
    -- as well as the code, so the panel can link to the place while the code
    -- keeps the row readable after it closes.
    counterpart_location_id bigint REFERENCES locations (id) ON DELETE SET NULL,
    counterpart_code        text   NOT NULL DEFAULT '',
    -- No foreign key, like outbox_events.aggregate_id: checkout reserves stock
    -- before the orders row exists, inside the same transaction, and those rows
    -- are completed by attachMovementsToOrder before anyone can read them.
    order_id       bigint,
    order_number   text        NOT NULL DEFAULT '',
    -- SET NULL for superuser_invitations.invited_by's reason (M18): who moved
    -- the stock is a fact about the past and outlives their account.
    -- actor_email is what survives them, so a null id with an address reads as
    -- "somebody who has since left" and a null id with none reads as "the
    -- system". It is a snapshot for the same reason admin_audit's is: an
    -- operator who changes their address leaves the old one on old rows.
    superuser_id   bigint      REFERENCES superusers (id) ON DELETE SET NULL,
    actor_email    text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- A row here says something happened. An untracked variant's reservation
    -- moves nothing by design, and a re-run of an unchanged import changes
    -- nothing — a ledger padded with those hides the rows that matter. The one
    -- exception is a stock take: an operator who counted a shelf and found the
    -- number it already had has produced evidence, and that is one of the two
    -- questions this table exists to answer.
    CONSTRAINT stock_movements_says_something
        CHECK (on_hand_delta <> 0 OR reserved_delta <> 0 OR kind = 'stock_take')
);

-- id, not created_at: a transfer writes both of its rows at the same instant, so
-- the serial is the only total ordering. This is outbox_aggregate_idx's shape
-- and outbox_aggregate_idx's reason — per-parent history, newest first.
CREATE INDEX stock_movements_variant_idx  ON stock_movements (variant_id, id DESC);
CREATE INDEX stock_movements_location_idx ON stock_movements (location_id, id DESC);
-- Partial, because most rows are operator movements with no order at all.
CREATE INDEX stock_movements_order_idx    ON stock_movements (order_id, id DESC)
    WHERE order_id IS NOT NULL;

-- Every balance that already exists gets one row explaining it, or the ledger
-- begins by disagreeing with the shelf: sum(on_hand_delta) per (variant,
-- location) is the invariant the doctor's stock-ledger check runs, and an
-- unexplained opening balance would break it for every variant in the store on
-- day one.
--
-- created_at is the upgrade time and not the stock's real age. That is the only
-- honest thing this migration knows — updated_at is when the balance last
-- CHANGED, which for an untouched row is when M17 backfilled it — so the reason
-- text says what the date means. An operator reading a variant's history on day
-- two will see one 'opening' row where they expected months of receipts.
--
-- Rows already at 0/0 are bookkeeping, not stock, and get nothing: their sum is
-- 0 either way, so the invariant holds without them.
INSERT INTO stock_movements (variant_id, sku, location_id, location_code, kind, source,
                             on_hand_delta, reserved_delta, on_hand_after, reserved_after,
                             reason)
SELECT vs.variant_id, v.sku, vs.location_id, l.code, 'opening', 'migration',
       vs.on_hand, vs.reserved, vs.on_hand, vs.reserved,
       'balance carried forward when the ledger began'
FROM variant_stock vs
JOIN variants v  ON v.id = vs.variant_id
JOIN locations l ON l.id = vs.location_id
WHERE vs.on_hand <> 0 OR vs.reserved <> 0;
`

// M27 — where a product sits inside a collection.
//
// product_collections had one `position` column and two readings of it had
// grown. SetProductCollections writes it from the index of the PRODUCT's own
// collection list, and loadProductCollections reads it back that way to order a
// product's chips. ListProducts read the same column as the product's rank
// INSIDE ONE COLLECTION, under the comment "a collection is curated by hand".
// One integer cannot carry both, and only the first meaning was ever written —
// so a collection's order was an artefact of how many other collections each of
// its members happened to be in, which is not an order anybody chose.
//
// `position` keeps the first reading, untouched. This column is the second, and
// it is what PUT /api/admin/collections/{id}/products writes (D40).
const migration0027CollectionCuration = migration0027Alter + curationBackfill + migration0027Index

const migration0027Alter = `
ALTER TABLE product_collections
    ADD COLUMN member_position integer NOT NULL DEFAULT 0;
`

// curationBackfill freezes exactly what stores can see today. It has a name of
// its own so the migration's test runs the statement the migration ran rather
// than a copy of it that can drift.
//
// The pre-migration visible order is `ORDER BY pc.position, pc.product_id` — the
// shipped expression, NOT product_id alone. A product that belongs to three
// collections carries member positions 0, 1 and 2 across its three rows, so
// numbering by product_id would silently reshuffle every collection whose
// members sit in more than one, including the storefront order served by
// GET /api/collections/{slug}. Migrations are append-only, so that mistake
// would have been unfixable in place.
//
// Numbering by the shipped expression means nothing an operator is looking at
// moves on upgrade, and a product added afterwards lands at the end rather than
// in the middle of a list somebody has been reading.
const curationBackfill = `
UPDATE product_collections pc
SET member_position = ranked.rn - 1
FROM (
    SELECT product_id, collection_id,
           row_number() OVER (
               PARTITION BY collection_id ORDER BY position, product_id) AS rn
    FROM product_collections
) ranked
WHERE pc.product_id = ranked.product_id
  AND pc.collection_id = ranked.collection_id;
`

const migration0027Index = `
CREATE INDEX product_collections_member_idx
    ON product_collections (collection_id, member_position, product_id);
`

// M28 — an abandoned cart becomes a record instead of a deletion.
//
// The 'abandoned' status has been in the M2 CHECK since the first migration and
// nothing has ever written it: checkout writes 'converted' and the sweeper
// DELETEd every open cart past its TTL. That destroyed the store's
// second-most-valuable report — the baskets somebody filled and did not come
// back to, their value, and the address to reach the shopper on — on a
// five-minute ticker.
//
// Deleting was not laziness. POST /api/carts is unauthenticated, so unswept
// carts are an unbounded-growth vector rather than merely untidy, and keeping
// them needs its own bound. There are two, and they are what make this safe: a
// cart holding no lines is still deleted outright (nothing to recover, and it
// is the shape probe traffic takes — a cart may be created with no body at
// all), and an abandoned cart is purged once Config.CartRetention has passed.
// What survives is "baskets somebody actually filled, for a fixed window".
//
// abandoned_at rather than reusing updated_at, for two reasons. updated_at
// means "when the shopper last changed this", and a report whose job is "this
// sat for five days before it was given up on" cannot say that if the sweep
// overwrote it — so the sweep deliberately leaves updated_at alone. And it is
// the retention clock: running retention off expires_at would abandon and purge
// a backlogged cart inside the same pass, announcing a live recovery token for
// a row that no longer exists.
//
// The CHECK is a biconditional on purpose. A row marked abandoned with no
// abandoned_at matches no purge predicate and never expires — an immortal row
// with somebody's email in it. A row that kept its abandoned_at through a
// revival gets purged out from under a live shopper. Both are silent, and both
// are now unreachable rather than merely unlikely. It validates for free: every
// existing row is open or converted with abandoned_at NULL, so both sides are
// false.
//
// No backfill. Every expired cart already in the table stays 'open' and is
// picked up by Abandon on its next pass, so every abandonment goes through the
// service and therefore has an event. A migration that marked them directly
// would produce a thousand abandoned carts nobody was told about, which is the
// exact failure the outbox exists to prevent.
const migration0028CartAbandonment = `
ALTER TABLE carts ADD COLUMN abandoned_at timestamptz;

ALTER TABLE carts
    ADD CONSTRAINT carts_abandoned_at_matches_status
    CHECK ((status = 'abandoned') = (abandoned_at IS NOT NULL));

-- The purge sweep's claim. Partial, mirroring carts_expiry_idx (M2) for the
-- other status: that one cannot see an abandoned row, and this one has no
-- business carrying the live ones.
CREATE INDEX carts_abandoned_idx ON carts (abandoned_at) WHERE status = 'abandoned';

-- The admin list: filtered by status, newest first, paged. Leading on id rather
-- than updated_at because touchCart bumps updated_at on every add-to-cart, and
-- an admin screen must not re-key an index on the hottest public write path in
-- the engine. Together with carts_expiry_idx this is also what serves the
-- derived state='abandoned' filter, as a BitmapOr of the two.
CREATE INDEX carts_admin_idx ON carts (status, id DESC);
`

// M29 — the orderings an operator can now ask for.
//
// Sorting adds no columns and no tables: it is a read, and every field the
// panel offers already exists. What it adds is orderings the planner would
// otherwise answer by reading and sorting the whole table — fine at fifty
// products, not fine at fifty thousand, and LIMIT/OFFSET repeats that sort on
// every page rather than once for the walk.
//
// Each index is (sort key, id) and ascending only, and every indexed key is a
// NOT NULL expression carrying no NULLS clause in the ORDER BY. Both halves of
// that sentence are load-bearing. The tiebreaker takes the same direction as
// the sort (SortSpec.Clause), so the ORDER BY never asks for a mixed ASC/DESC
// ordering, which is the shape a plain two-column btree cannot serve. And
// because the expression is never NULL, the clause states no NULLS placement,
// so the query's defaults — ASC NULLS LAST, DESC NULLS FIRST — are exactly what
// a forward and a backward scan of this index produce. Add "NULLS LAST" to one
// of these ORDER BYs and the descending direction silently stops using the
// index; that is why the nullable keys (a discount with no code, an order with
// no name, a product with no tracked variants, media referenced by URL) pin
// NULLS LAST per field and are deliberately not indexed here.
//
// Only the tables that grow are here. Discounts, tax rates and locations are
// tens of rows; an index there costs every write something to save a scan that
// was never slow. Customers and the category search are unindexable by
// construction — one sorts the output of a GROUP BY, the other the output of a
// recursive CTE, and both compute every key before ordering can begin.
//
// IF NOT EXISTS because applyMigration runs this inside InTx, so CREATE INDEX
// CONCURRENTLY is not available here and a bare CREATE INDEX holds a SHARE lock
// while it builds — on a large orders table that is blocked checkouts at boot.
// A store that big builds these CONCURRENTLY out of band with the identical
// definitions before deploying; without IF NOT EXISTS that prudence would fail
// the migration and the boot.
//
// Deliberately absent, recorded so the next reader knows they were considered:
// products (created_at, id) is co-monotonic with id in practice, so ?sort=id is
// the indexed proxy for "newest first"; orders_created_idx already leads with
// created_at DESC; media's filename expression carries an explicit NULLS LAST,
// which an ASC btree cannot serve in both directions; a discount table is tens
// of rows; and every status or kind key would want products_status_idx
// (status, id DESC) or media_kind_idx (kind, id DESC), mixed orderings matching
// neither scan direction — and three to five distinct values make an index
// useless for ordering anyway.
const migration0029SortIndexes = `
CREATE INDEX IF NOT EXISTS products_title_sort_idx   ON products (lower(title), id);
CREATE INDEX IF NOT EXISTS products_updated_sort_idx ON products (updated_at, id);
CREATE INDEX IF NOT EXISTS orders_total_sort_idx     ON orders   (total_minor, id);
CREATE INDEX IF NOT EXISTS media_size_sort_idx       ON media    (size_bytes, id);

-- Not for ordering variants — variants_product_idx (product_id, position, id)
-- already does that — but for the per-product minimum the product list sorts
-- by. That index does not carry price_minor, so min(v.price_minor) is a heap
-- fetch per variant; this makes it one index-only lookup per product.
CREATE INDEX IF NOT EXISTS variants_product_price_idx ON variants (product_id, price_minor);
`

// M30 — the two lookups the events screen makes, which the dispatcher's own
// index cannot serve.
//
// outbox_unpublished_idx (M3) is partial on `published_at IS NULL AND NOT
// dead`, which is exactly the half an operator does not need: the rows worth
// looking at are the ones it excludes. Neither state nor event_name has an
// index, over a table that keeps every delivered event forever.
//
// IF NOT EXISTS earns its place here rather than by following M29. This is the
// first migration whose DDL locks a table that every checkout writes to, and a
// migration runs inside one transaction (migrate.go) so CONCURRENTLY is
// unavailable here. IF NOT EXISTS is therefore the escape hatch: a store with a
// large outbox builds the identical index with CREATE INDEX CONCURRENTLY before
// deploying, and this migration finds it already there and does nothing. That
// only reaches somebody if the release note carries the two statements, so it
// must.
const migration0030OutboxIndexes = `
-- Partial, so it costs an insert nothing: a row is written dead = false and
-- never enters this index until something parks it.
--
-- The predicate carries published_at IS NULL as well as dead, matching the
-- screen's dead filter rather than the bare column. A row can hold both flags —
-- markPublished does not clear dead, fail does not check published_at, and a
-- slow batch outlives its sixty-second visibility window — and published wins,
-- so a row that raced is a delivered event and not a dead letter. The same
-- predicate in the index, the filter and the remaining count, so all three
-- agree and the count is an index-only scan.
CREATE INDEX IF NOT EXISTS outbox_dead_idx ON outbox_events (id)
    WHERE dead AND published_at IS NULL;

-- Full, and it is the one insert cost this migration adds: one btree entry per
-- event. It buys the question a broken consumer forces — "every order.paid,
-- what happened to them" — staying constant-time as history grows, instead of a
-- sequential scan over every event the store has ever delivered.
CREATE INDEX IF NOT EXISTS outbox_name_idx ON outbox_events (event_name, id);
`

// M31 — shipping zones and rates (D52).
//
// Until now `shipping_minor` came from one number in `Config`, which is a
// store that can charge one price to everyone everywhere. A zone says where,
// a rate says what a named method costs there, and the band on the rate is
// what lets "Standard 49, free over 2000" be one method rather than a special
// case in the checkout.
//
// The zone's matchers are arrays because a zone is a set of places — "EU" is
// twenty-seven countries and one price. Empty means "anywhere", the way an
// empty country on a tax rate is the fallback every other rule beats.
//
// The band is closed at the bottom and open at the top: `max_subtotal_minor`
// NULL is "no ceiling", and a rate applies when the basket is at or above the
// minimum and strictly below the maximum. Half-open so that two bands meeting
// at 200000 cannot both match the basket that is exactly 200000 — the
// alternative is a rule nobody can hold in their head at the moment they most
// need to.
const migration0031Shipping = `
CREATE TABLE shipping_zones (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL CHECK (length(btrim(name)) > 0),
    countries  text[] NOT NULL DEFAULT '{}',
    states     text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE shipping_rates (
    id                 bigserial PRIMARY KEY,
    zone_id            bigint NOT NULL REFERENCES shipping_zones(id) ON DELETE CASCADE,
    name               text NOT NULL CHECK (length(btrim(name)) > 0),
    price_minor        bigint NOT NULL CHECK (price_minor >= 0),
    min_subtotal_minor bigint NOT NULL DEFAULT 0 CHECK (min_subtotal_minor >= 0),
    max_subtotal_minor bigint CHECK (max_subtotal_minor IS NULL OR max_subtotal_minor > min_subtotal_minor),
    active             boolean NOT NULL DEFAULT true,
    position           integer NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX shipping_rates_zone ON shipping_rates (zone_id, position, id);

-- What the order was actually sold. A snapshot for the reason every other
-- snapshot here exists: the rate can be renamed, repriced or deleted, and an
-- order must still say what the shopper agreed to pay for.
ALTER TABLE orders ADD COLUMN shipping_method text NOT NULL DEFAULT '';
`

// M32 — how big the box is.
//
// Dimensions follow weight's rule, which follows money's: one canonical stored
// value per side plus the unit they are read in. The millimetres are the fact a
// carrier API is given; `dimension_unit` is presentation, exactly as
// `weight_unit` is for the mass beside them.
//
// Millimetres rather than centimetres because it is the resolution carriers
// quote in, and an integer count of them needs no conversion to be correct.
//
// One unit for all three sides rather than one each. A box is measured in a
// single unit by whoever holds the tape, and three independent units would
// make "30 × 20 × 45" unreadable without three more lookups.
//
// NULL rather than 0 for an unmeasured side, and this is the distinction that
// earns the nullable column: a zero-height parcel is a carrier error, while an
// unmeasured one is paperwork still to do. A NOT NULL DEFAULT 0 would make
// every variant in every existing store claim to be a flat sheet.
//
// No index. Nothing searches or sorts on a side — these are read with the
// variant that owns them and sent to a carrier, which is the same access path
// weight_grams has had since M1.
const migration0032VariantDimensions = `
ALTER TABLE variants
    ADD COLUMN length_mm integer CHECK (length_mm IS NULL OR length_mm >= 0),
    ADD COLUMN width_mm  integer CHECK (width_mm  IS NULL OR width_mm  >= 0),
    ADD COLUMN height_mm integer CHECK (height_mm IS NULL OR height_mm >= 0),
    ADD COLUMN dimension_unit text NOT NULL DEFAULT 'mm'
        CHECK (dimension_unit IN ('mm', 'cm', 'm', 'in'));
`

// M33 — finding products by what a category asked about them.
//
// The answers have been storable since the taxonomy landed and unreadable ever
// since: they sit in `products.metadata.category` as
// `{handle: [values]}`, and nothing queried them, so a store could
// record a material on five hundred products and not list the canvas ones.
//
// The filter asks containment — does this product's answers object contain
// `{"bag-material": ["Canvas"]}"` — which is one operator rather
// than a lateral unnest per attribute, and which a GIN index can answer.
//
// jsonb_path_ops rather than the default operator class: it stores a hash per
// path rather than an entry per key AND per value, which is roughly a third the
// size and faster for exactly the `@>` this asks. What it gives up is
// the key-existence operators (`?`, `?|`, `?&`), which this query does
// not use — an attribute filter always names a value.
//
// The index is on the expression `(metadata -> 'category')` rather
// than on the whole column, because the rest of metadata is a module's
// scratch space and an operator's free-form metafields: indexing those would
// pay for writes nobody searches.
const migration0033AttributeIndex = `
CREATE INDEX products_category_attrs_idx
    ON products USING gin ((metadata -> 'category') jsonb_path_ops);
`

// M34 — who is buying, and how many.
//
// Until now a variant had exactly one price and the whole money path leaned on
// that: AddLine snapshots `variants.price_minor`, checkout re-prices to it under
// the lock, and the difference between the two is what makes a shopper
// re-confirm. Nothing here changes that shape — it changes what "the current
// price" resolves to, and leaves the single source intact.
//
// Membership keys on the email rather than on a customer id because this engine
// has no customers table: a customer is an address that has ordered, which is
// what customers.go reads and the only handle a group can hold. It is folded to
// lower case on the way in, because a cart carries whatever the shopper typed.
//
// A list with no group is everybody's — a launch price, a seasonal one — so
// group_id is nullable and NULL means "applies to all", not "applies to none".
// Getting that backwards would make every ungrouped list silently dead.
//
// The window columns are nullable at both ends: most lists are open-ended in one
// direction or both, and a NOT NULL default would make "from now until further
// notice" something an operator has to express with a date in 2099.
//
// ON DELETE CASCADE from group to list is deliberate where the catalogue uses
// RESTRICT. A category with products under it is refused because the products
// are the valuable thing and would be orphaned; a price list whose audience has
// gone is not orphaned, it is meaningless — it would price for nobody and could
// never be reached again.
const migration0034Pricing = `
CREATE TABLE customer_groups (
    id         bigserial   PRIMARY KEY,
    code       text        NOT NULL UNIQUE CHECK (code <> ''),
    name       text        NOT NULL CHECK (name <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE customer_group_members (
    group_id bigint      NOT NULL REFERENCES customer_groups (id) ON DELETE CASCADE,
    -- Stored folded; the service lowers it on the way in so that the primary
    -- key does the de-duplication rather than a query having to remember.
    email    text        NOT NULL CHECK (email <> '' AND email = lower(email)),
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, email)
);
CREATE INDEX customer_group_members_email_idx ON customer_group_members (email);

CREATE TABLE price_lists (
    id         bigserial   PRIMARY KEY,
    name       text        NOT NULL CHECK (name <> ''),
    -- NULL is everybody.
    group_id   bigint      REFERENCES customer_groups (id) ON DELETE CASCADE,
    starts_at  timestamptz,
    ends_at    timestamptz,
    active     boolean     NOT NULL DEFAULT true,
    -- Higher wins when two lists both cover a line.
    priority   integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT price_lists_window
        CHECK (starts_at IS NULL OR ends_at IS NULL OR ends_at > starts_at)
);
CREATE INDEX price_lists_live_idx ON price_lists (group_id) WHERE active;

CREATE TABLE price_list_prices (
    price_list_id bigint  NOT NULL REFERENCES price_lists (id) ON DELETE CASCADE,
    variant_id    bigint  NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    -- The quantity break. 1 is "any quantity", which is why it is the default
    -- rather than 0: a break at zero units is not a thing anybody sells.
    min_quantity  integer NOT NULL DEFAULT 1 CHECK (min_quantity >= 1),
    amount_minor  bigint  NOT NULL CHECK (amount_minor >= 0),
    PRIMARY KEY (price_list_id, variant_id, min_quantity)
);
-- The resolution query starts from the variant, so this is the index it needs.
CREATE INDEX price_list_prices_variant_idx ON price_list_prices (variant_id, min_quantity);
`

// M35 — one catalogue, several storefronts.
//
// The decision the rest of this hangs off is what an unpublished product means,
// and it is the opposite of the obvious one. A product with no row in
// product_channels belongs to EVERY channel, not to none.
//
// The obvious reading — a join table that grants visibility — would empty every
// existing catalogue the instant a store created its first channel, because no
// product would have a row yet. Every store that adopted channels would take
// its shop down and only find out from the sales figures. So the table records
// a *narrowing* an operator opts into product by product, and a product that
// has never been asked about stays where it was: everywhere.
//
// The same shape as M34's nullable group_id, and for the same reason: the empty
// state has to mean "no restriction" rather than "no access", or adopting the
// feature is a breaking change disguised as a migration.
//
// Currency deliberately does not appear here. D14 settled one settlement
// currency per store and snapshots it onto every order; per-channel currency
// would reopen that decision on the money path, and a channel that differs only
// in what is published and what it costs is the whole of what this claims to be.
//
// The default channel is enforced by a partial unique index rather than by
// application code, because two defaults is a state with no meaning — a request
// that names no channel would have no answer — and the database is the only
// place that can refuse it under concurrency.
const migration0035Channels = `
CREATE TABLE channels (
    id         bigserial   PRIMARY KEY,
    code       text        NOT NULL UNIQUE CHECK (code <> ''),
    name       text        NOT NULL CHECK (name <> ''),
    active     boolean     NOT NULL DEFAULT true,
    is_default boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX channels_one_default_idx ON channels (is_default) WHERE is_default;

-- A narrowing, not a grant. No rows for a product means every channel.
CREATE TABLE product_channels (
    product_id bigint NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    channel_id bigint NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    PRIMARY KEY (product_id, channel_id)
);
CREATE INDEX product_channels_channel_idx ON product_channels (channel_id);

-- Which storefront opened this basket, and which sold this order. Nullable
-- because every cart and order that already exists was opened before channels
-- did, and backfilling them into a channel invented after the sale would be a
-- fact the store never recorded.
ALTER TABLE carts  ADD COLUMN channel_id bigint REFERENCES channels (id) ON DELETE SET NULL;
ALTER TABLE orders ADD COLUMN channel_id bigint REFERENCES channels (id) ON DELETE SET NULL;
CREATE INDEX orders_channel_idx ON orders (channel_id) WHERE channel_id IS NOT NULL;

-- A price list may narrow to one channel. NULL is every channel, exactly as
-- NULL group_id is everybody.
ALTER TABLE price_lists ADD COLUMN channel_id bigint REFERENCES channels (id) ON DELETE CASCADE;
`

// M36 — a variant's pictures.
//
// M9 gave a variant one picture by putting `variant_id` on the product_media
// row, which made two things impossible that a real catalogue needs: a variant
// with several pictures, and two variants sharing one. The second is the
// common case, not the edge — every size of a colour shows that colour's
// photographs — and under M9 the last size to nominate a photograph took it
// from the one before, so a forty-variant shirt ended up with pictures on
// seven of them.
//
// So the nomination moves to its own table, one row per (variant, picture),
// ordered. The invariant stays: a variant shows pictures the product already
// has, enforced in media.go rather than by a constraint, because the check
// needs the variant's product and a foreign key cannot say that. RESTRICT on
// the file for product_media's reason, though in practice product_media
// refuses first. CASCADE on the variant, because a variant's list is nothing
// without the variant; the product still has the file either way.
//
// The column goes. Nothing reads it once this runs, and a column that is
// still there but no longer true is how the next reader gets it wrong.
const migration0036VariantPictures = `
CREATE TABLE variant_media (
    variant_id bigint  NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    media_id   bigint  NOT NULL REFERENCES media (id) ON DELETE RESTRICT,
    position   integer NOT NULL DEFAULT 0,
    PRIMARY KEY (variant_id, media_id)
);
CREATE INDEX variant_media_media_idx ON variant_media (media_id);

INSERT INTO variant_media (variant_id, media_id)
    SELECT variant_id, media_id FROM product_media WHERE variant_id IS NOT NULL;

DROP INDEX product_media_variant_key;
DROP INDEX product_media_variant_idx;
ALTER TABLE product_media DROP COLUMN variant_id;
`

// M37 — whether a thing is sent at all.
//
// A download, a service or a gift card is sold like anything else and never
// leaves a shelf. Until now the checkout could not tell, so a basket of
// nothing but downloads paid postage and was refused where no zone covered
// the address. The flag sits on the variant, for M10's reason: a product can
// sell a printed edition beside the PDF. It defaults to true, which is what
// every existing variant is, and Shopify's column of the same meaning
// (Variant Requires Shipping) now has a home in both directions.
const migration0037RequiresShipping = `
ALTER TABLE variants ADD COLUMN requires_shipping boolean NOT NULL DEFAULT true;
`

// M38 — plugins: the switch and the settings, one row each.
//
// A plugin's descriptor — its name, its fields — lives in code, in core for
// the storefront features that are nothing but settings and in the module
// that implements the rest. The row is only what an operator decided: on or
// off, and the values. No row means the descriptor's default, so a store
// that has never opened the page behaves as it did before the page existed.
const migration0038Plugins = `
CREATE TABLE plugins (
    key        text        PRIMARY KEY,
    enabled    boolean     NOT NULL DEFAULT false,
    settings   jsonb       NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now()
);
`

// M39 — what the store told its shoppers.
//
// One row per delivery, written at the funnel every notification passes
// through, after the backends have answered: the event, the channel, the
// recipient, the backend that carried it, and whether it went. The question
// that brings an operator here is "did she get her confirmation", and until
// now the only answer was a log line on a server the operator cannot read.
// `data` keeps the template variables so a message can be sent again as it
// was; `resend_of` ties the second attempt to the first.
const migration0039Notifications = `
CREATE TABLE notifications (
    id           bigserial   PRIMARY KEY,
    event        text        NOT NULL,
    channel      text        NOT NULL,
    recipient    text        NOT NULL,
    language     text        NOT NULL DEFAULT '',
    order_number text        NOT NULL DEFAULT '',
    backend      text        NOT NULL DEFAULT '',
    status       text        NOT NULL CHECK (status IN ('sent', 'logged', 'failed')),
    error        text        NOT NULL DEFAULT '',
    data         jsonb       NOT NULL DEFAULT '{}',
    resend_of    bigint      REFERENCES notifications (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notifications_created_idx ON notifications (id DESC);
CREATE INDEX notifications_order_idx   ON notifications (order_number) WHERE order_number <> '';
CREATE INDEX notifications_status_idx  ON notifications (status, id DESC);
`

// M40 — the wording of what the store sends.
//
// The catalogue of messages and their default subject and body live in code
// (core for the engine's events, a module for its own). This table holds only
// what an operator changed: one row per message they reworded, keyed by the
// channel and the event. No row means the default, and a delete restores it —
// which is the property that makes the editor safe to hand to anyone with
// store.operate.
const migration0040NotificationTemplates = `
CREATE TABLE notification_templates (
    channel    text        NOT NULL,
    event      text        NOT NULL,
    subject    text        NOT NULL DEFAULT '',
    body       text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel, event)
);
`

// M41 — what a store calls its roles.
//
// The three roles are fixed in Go (rights.go) and that does not change here:
// `owner`, `manager` and `staff` are written on every superuser row and in
// role_rights, so the key is an identifier and renaming one would orphan
// accounts. What a store may change is what it *calls* them, and what it says
// they are for — "Fulfilment" reads better than "Staff" in a warehouse, and a
// description is where a store writes down the rule it actually operates.
//
// A row per role only where the store has said something, which is the same
// shape role_rights uses and for the same reason: no row means the engine's
// own words, so improving a default reaches every store that never edited it.
//
// No CHECK on the role name, unlike role_rights. That constraint exists there
// to keep `owner` unstorable, because storing owner's rights would be a way to
// lock everybody out. A title is not a right: naming the owner role
// "Proprietor" locks nobody out of anything, so all three are storable.
const migration0041RoleProfiles = `
CREATE TABLE role_profiles (
    role        text        PRIMARY KEY,
    title       text        NOT NULL DEFAULT '',
    description text        NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- SET NULL for the reason role_rights gives: who renamed a role is a fact
    -- about the past and outlives their account.
    updated_by  bigint      REFERENCES superusers (id) ON DELETE SET NULL
);
`

// M42 — the shop's own details.
//
// Everything on the Store settings screen until now came from the Config the
// binary was started with: the currency, the languages, the TTLs. Those are
// start-up decisions and are right to be read-only. The shop's *name* is not
// one of them, and neither is its address, its contact email or its tax
// registration — those are facts about a business that change without a
// redeploy, and they belong on invoices, in emails and at the foot of a
// storefront.
//
// Until this table they lived nowhere. ext/invoices took the seller's name and
// address from its own module Config, which meant a shop editing its address
// had to be restarted with new environment variables, and any other module
// that wanted the same facts had to be given them again.
//
// One row, and the CHECK is what keeps it one: a second row would give the
// store two identities and nothing would say which was current.
const migration0042StoreProfile = `
CREATE TABLE store_profile (
    id           integer     PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    name         text        NOT NULL DEFAULT '',
    legal_name   text        NOT NULL DEFAULT '',
    email        text        NOT NULL DEFAULT '',
    phone        text        NOT NULL DEFAULT '',
    address_line1 text       NOT NULL DEFAULT '',
    address_line2 text       NOT NULL DEFAULT '',
    city         text        NOT NULL DEFAULT '',
    state        text        NOT NULL DEFAULT '',
    postal_code  text        NOT NULL DEFAULT '',
    country      text        NOT NULL DEFAULT '',
    tax_id       text        NOT NULL DEFAULT '',
    support_url  text        NOT NULL DEFAULT '',
    updated_at   timestamptz NOT NULL DEFAULT now(),
    -- SET NULL for the reason role_rights gives: who changed the shop's
    -- address is a fact about the past and outlives their account.
    updated_by   bigint      REFERENCES superusers (id) ON DELETE SET NULL
);
INSERT INTO store_profile (id) VALUES (1);
`

// M43 — the store keeps its own clock and its own voice.
//
// A new migration rather than columns added to M42, because a shipped
// migration is frozen: a store that has already run 42 would never see an
// edit to it.
//
// Both default to empty, and empty is a real answer rather than a gap to fill
// in. No timezone means UTC, which is what the server was doing anyway; no
// language means the one the binary was started with. A store that has never
// opened the screen behaves exactly as it did before this ran.
const migration0043StoreClock = `
ALTER TABLE store_profile
    ADD COLUMN timezone text NOT NULL DEFAULT '',
    ADD COLUMN language text NOT NULL DEFAULT '';
`

// M44 — API keys: a credential with a role.
//
// Config.AdminTokens already exists and is deliberately roleless: it is what a
// deploy script uses, there is nobody behind it, and requireRights waves it
// through everything. That is right for a script on the same machine and wrong
// for the thing a store actually wants, which is a key it can give a shipping
// partner that reads orders and cannot refund them.
//
// The secret is never stored. The prefix is, in plain text and indexed, so a
// presented key can be found in one lookup and so the panel can show which key
// is which; the hash beside it is what decides whether the rest of the key was
// right. Finding the row is therefore cheap and forging one still costs a
// preimage.
//
// A revoked key keeps its row. Deleting it would erase the only record of what
// had access and when it stopped, which is the question asked after an
// incident rather than before one.
const migration0044APIKeys = `
CREATE TABLE api_keys (
    id           bigserial   PRIMARY KEY,
    name         text        NOT NULL,
    -- The visible half, unique so a presented key resolves in one lookup.
    prefix       text        NOT NULL UNIQUE,
    -- SHA-256 of the whole presented key, hex. Not bcrypt: this is machine-
    -- generated randomness rather than a password somebody chose, so there is
    -- no dictionary to slow down, and a key is checked on every request.
    token_hash   text        NOT NULL,
    role         text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- SET NULL for the reason store_profile gives: who issued a key is a fact
    -- about the past and outlives their account.
    created_by   bigint      REFERENCES superusers (id) ON DELETE SET NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz,
    revoked_by   bigint      REFERENCES superusers (id) ON DELETE SET NULL
);
CREATE INDEX api_keys_active_idx ON api_keys (revoked_at, id DESC);
`

// M45 — custom reports: a SELECT somebody saved.
//
// The SQL is stored as text and never interpolated into anything. It is not
// a query template with parameters, because a report an operator can
// parameterise is a report an operator can rewrite at call time, and the
// point of saving one is that what runs is what was reviewed.
//
// No unique constraint on the name. Two reports called "Monthly" are a mess
// somebody made and can fix; refusing the second is a rule that fires on the
// person typing rather than on the person who typed first.
const migration0045CustomReports = `
CREATE TABLE custom_reports (
    id          bigserial   PRIMARY KEY,
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    sql         text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- SET NULL for the reason store_profile gives: who wrote a report is a
    -- fact about the past and outlives their account.
    created_by  bigint      REFERENCES superusers (id) ON DELETE SET NULL,
    updated_by  bigint      REFERENCES superusers (id) ON DELETE SET NULL
);
`

// M46 — keep the key, not only its hash.
//
// M44 hashed the secret and threw it away, which is what GitHub and Stripe
// do and is the stricter thing. It was also stricter than anything else in
// this engine: a live Stripe secret key, a SendGrid key and every other
// plugin credential are stored recoverably in `plugins` and merely masked on
// the way out. So hashing this one bought a property the rest of the system
// does not have, while costing an operator their key the moment they
// navigated away from the screen that made it.
//
// token_hash stays and is still what authenticates. This column is only for
// showing the operator the key again, behind apikeys.write — knowing a key
// exists and seeing the credential are different acts, and only the second
// one is owner-shaped.
//
// Keys issued before this ran have ” here and cannot be recovered, which is
// the truth about them; the screen says so rather than showing a blank box.
const migration0046APIKeySecret = `
ALTER TABLE api_keys
    ADD COLUMN secret text NOT NULL DEFAULT '';
`

// M47 — vendors, and what they offer.
//
// A vendor here is a seller on this store, not a brand. The two are different
// things and the engine already had the other one: `products.vendor` is free
// text holding "Anker" or "Amazon Essentials", it is what the Google feed sends
// as `brand`, and it stays exactly as it is. Anker is not a seller on anyone's
// marketplace, and backfilling manufacturer names into this table would have
// asserted that it was.
//
// Two tables, because a seller and a thing a seller sells are not the same
// record and only one of them is per-variant.
//
// ---------------------------------------------------------------------------
// Why offers are a table and not columns on `variants`
//
// The alternative was `variants.vendor_id`, one seller per sellable thing. That
// is the simpler schema and it is the wrong one for a marketplace: it says two
// sellers of the same trainers are two products, so the shop shows the trainers
// twice and no shopper can compare the price. An offer is the row that lets one
// catalogue entry carry several sellers.
//
// The variant keeps its own `price_minor` and `stock_on_hand`. That is
// deliberate and it is what makes this migration additive: a store with no
// offers behaves exactly as it did yesterday, and the variant's own numbers are
// the store's first-party offer — the shop selling its own stock, which is the
// case for every store that exists today and most that ever will. Moving price
// and stock off the variant wholesale would have been a flag day across the
// cart, the checkout, the movement ledger, the reports and both transfer
// dialects, all at once, to support a feature nobody has switched on yet.
//
// ---------------------------------------------------------------------------
// What this migration does NOT do
//
// Nothing sells from an offer yet. Checkout still prices and reserves against
// the variant, because teaching the reservation path to hold stock against a
// particular seller is the next piece of work and it is not a schema change.
// The columns for it are here now rather than added later — `stock_reserved`
// and the constraint that keeps it inside `stock_on_hand`, mirroring
// `variants` — because migrations are append-only and the shape is cheaper to
// get right once than to correct across three more of them.
const migration0047Vendors = `
CREATE TABLE vendors (
    id            bigserial   PRIMARY KEY,
    -- The handle a storefront puts in a URL. Unique because /vendor/{slug} has
    -- to resolve to one seller.
    slug          text        NOT NULL UNIQUE,
    name          text        NOT NULL,
    -- The name on the invoice, when it differs from the one over the shop.
    legal_name    text        NOT NULL DEFAULT '',
    email         text        NOT NULL DEFAULT '',
    phone         text        NOT NULL DEFAULT '',
    website       text        NOT NULL DEFAULT '',
    about         text        NOT NULL DEFAULT '',
    -- SET NULL rather than RESTRICT: deleting a picture should not be blocked
    -- by a vendor using it, and a seller with no logo is an ordinary state.
    logo_media_id bigint      REFERENCES media (id) ON DELETE SET NULL,
    address_line1 text        NOT NULL DEFAULT '',
    address_line2 text        NOT NULL DEFAULT '',
    city          text        NOT NULL DEFAULT '',
    state         text        NOT NULL DEFAULT '',
    postal_code   text        NOT NULL DEFAULT '',
    country       text        NOT NULL DEFAULT '',
    tax_id        text        NOT NULL DEFAULT '',
    -- pending is the default because a marketplace that lists a seller the
    -- moment they sign up has no moderation step at all. Only approved sellers
    -- are offered to shoppers; suspended keeps the row and its history.
    status        text        NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'suspended')),
    -- Basis points, like discounts.value_bp, for the reason rule 6 gives about
    -- money: 250 is 2.5% and cannot drift the way 0.025 can. Stored now and
    -- settled by nobody — PLAN.md §39.10 defers marketplace settlements, so
    -- this records the agreement without pretending to act on it.
    commission_bp integer     NOT NULL DEFAULT 0
        CHECK (commission_bp >= 0 AND commission_bp <= 10000),
    metadata      jsonb       NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vendors_status_idx ON vendors (status, id DESC);

CREATE TABLE vendor_offers (
    id             bigserial   PRIMARY KEY,
    vendor_id      bigint      NOT NULL REFERENCES vendors (id) ON DELETE CASCADE,
    -- The offer is against the variant, not the product: sellers differ on the
    -- large one and agree on the small one, and price is per sellable thing.
    variant_id     bigint      NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    price_minor    bigint      NOT NULL CHECK (price_minor >= 0),
    -- In the store's own currency, like variants.price_minor. A marketplace
    -- whose sellers price in different currencies is a multi-currency store
    -- first, which PLAN.md §39.5 defers.
    stock_on_hand  integer     NOT NULL DEFAULT 0 CHECK (stock_on_hand >= 0),
    stock_reserved integer     NOT NULL DEFAULT 0 CHECK (stock_reserved >= 0),
    track_inventory boolean    NOT NULL DEFAULT true,
    status         text        NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'paused')),
    metadata       jsonb       NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    -- One offer per seller per variant. A seller who wants two prices for the
    -- same thing is describing two things.
    CONSTRAINT vendor_offers_one_per_variant UNIQUE (vendor_id, variant_id),
    CONSTRAINT vendor_offers_reserved_within_on_hand CHECK (stock_reserved <= stock_on_hand)
);
-- The question a product page asks: who sells this variant, cheapest first.
CREATE INDEX vendor_offers_variant_idx ON vendor_offers (variant_id, status, price_minor);
`

// M48 — a vendor can sign in.
//
// A seller who cannot reach the store is a row somebody else maintains on their
// behalf, which is a supplier list rather than a marketplace. So a vendor gets
// an operator account: the same table, the same password hashing, the same
// sessions, the same rights machinery. Building a second identity system beside
// `superusers` would mean a second password reset, a second session store and a
// second place for a login bug to live.
//
// Two constraints carry the whole idea.
//
// The role CHECK is dropped and rewritten rather than added to, because M13
// wrote the list of roles into the database on purpose — "a role it has never
// heard of should not be storable" — and that list has gained a member. This is
// the append-only rule working as intended: the old migration is untouched and
// this one states the change.
//
// The second is the one that matters. `(role = 'vendor') = (vendor_id IS NOT
// NULL)` says a vendor account names exactly one vendor, and that no other kind
// of account names any. Without it the schema permits an account with the
// vendor role and no vendor attached — and that account is the one every
// scoping rule fails open on, because "show me my own products" has no answer
// when there is no "my". It is a CHECK rather than a service rule for the same
// reason the reserved-stock constraint is: the service is the first line and
// the database is the last one.
//
// ON DELETE CASCADE, so removing a seller takes their logins with them. The
// alternative is an account that can still sign in and belongs to nobody.
const migration0048VendorAccounts = `
ALTER TABLE superusers
    ADD COLUMN vendor_id bigint REFERENCES vendors (id) ON DELETE CASCADE;

ALTER TABLE superusers DROP CONSTRAINT superusers_role_check;
ALTER TABLE superusers
    ADD CONSTRAINT superusers_role_check
        CHECK (role IN ('owner', 'manager', 'staff', 'vendor'));

ALTER TABLE superusers
    ADD CONSTRAINT superusers_vendor_account
        CHECK ((role = 'vendor') = (vendor_id IS NOT NULL));

-- "Who can sign in for this seller" is asked on every request a vendor makes.
CREATE INDEX superusers_vendor_idx ON superusers (vendor_id) WHERE vendor_id IS NOT NULL;

-- M19 wrote the overridable roles into the database too, and vendor is one now.
-- Owner is still absent from this list on purpose: an owner has every right by
-- definition, so a row narrowing them would be a row that cannot be honoured.
ALTER TABLE role_rights DROP CONSTRAINT role_rights_role_check;
ALTER TABLE role_rights
    ADD CONSTRAINT role_rights_role_check
        CHECK (role IN ('manager', 'staff', 'vendor'));
`
