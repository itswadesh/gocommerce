# Operations

The production architecture is one Go binary and one PostgreSQL database.
Everything else — Redis, search, object storage — is a choice you make later
when traffic or reliability justifies it, never a prerequisite.

```
        Internet
           │
      reverse proxy (TLS)
           │
   one or more Go processes
           │
       PostgreSQL
```

## Configuration

Only `DBURL` and one admin token are required.

| Setting | Default | Notes |
|---|---|---|
| `DBURL` | — | PostgreSQL connection string. Required. |
| `Addr` | `:8080` | Listen address. |
| `Currency` | `USD` | One settlement currency per store, ISO 4217. |
| `DefaultLanguage` / `Languages` | `en` / `["en"]` | What a store sells in until it chooses its own languages on Store settings (D87). Each request is negotiated against its store's list. |
| `AdminTokens` | — | At least one, unless `Dev`. Several allow rotation. |
| `AdminAuth` | bearer tokens | Replace to add sessions, OIDC or RBAC. |
| `SecretsKey` / `SecretsKeyPrevious` | none | `GOCOMMERCE_SECRETS_KEY` / `GOCOMMERCE_SECRETS_KEY_PREVIOUS`, environment only. Seals the secrets the engine reads back; unset, they are stored readable. See [Secrets at rest](#secrets-at-rest). |
| `CORSOrigins` | none | Browser origins allowed to call the API directly (`GOCOMMERCE_CORS_ORIGINS`, comma-separated, `*` for any). Unset, a storefront on another origin has to proxy. Credentials are never allowed. |
| `Brands` | none | `GOCOMMERCE_BRANDS`, a JSON array. Several names on one install, each with its own admin domain and stores; see [Several brands](#several-brands). Unset, one brand made of `PanelURL` and `BaseDomain`. |
| `AllowPrivateURLs` | `false` | Lets the addresses a store sets — webhook endpoints, provider API base URLs — be private ones (`GOCOMMERCE_ALLOW_PRIVATE_URLS`). Only where every store operator runs the server; see [Addresses a store sets](#addresses-a-store-sets). |
| `CartTTL` | 720h | How long an untouched cart survives. |
| `CartRetention` | 720h | How long an abandoned cart is kept before deletion, measured from `abandoned_at`. |
| `OrderTTL` | 24h | How long an unpaid order holds its stock. |
| `HandlerTimeout` | 10s | Bounds one event handler. |
| `OutboxBatchSize` / `OutboxPoll` | 100 / 1s | Dispatcher tuning. |
| `FlatShippingMinor` | 0 | v1's entire shipping calculation. |
| `OrderPrefix` | `GC-` | Prefixes human order numbers. |

Generate an admin token with real entropy:

```sh
export GOCOMMERCE_ADMIN_TOKEN=$(openssl rand -hex 32)
```

Rotating one is a two-step deploy: add the new token to `AdminTokens`
alongside the old, move clients over, then remove the old. There is no window
in which nothing works.

## Deploying

```sh
go build -o gocommerce ./cmd/gocommerce        # or your own main()
./gocommerce -db "$DATABASE_URL" migrate       # apply the schema
./gocommerce -db "$DATABASE_URL" serve
```

`New` applies pending migrations on start, so `migrate` is only needed if you
prefer schema changes as a separate deployment step. Either way it is safe to
run several instances at once: migrations take a PostgreSQL advisory lock, so
the first instance applies them and the others wait rather than racing.

**Two migrations are worth planning for on a large store.** M29 creates five
indexes on `products`, `orders`, `media` and `variants` so the admin listings
can be ordered by title, price, stock, size and total, and M30 creates two on
`outbox_events` for the Events screen. Every migration runs inside a
transaction, so `CREATE INDEX CONCURRENTLY` is not available to it and a plain
`CREATE INDEX` holds a SHARE lock while it builds — on a large `orders` table
that is blocked checkouts for the length of the boot, and `outbox_events` is
written by every checkout too.

Every statement in both is `IF NOT EXISTS`, which is what lets a store that big
build them out of band first:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS products_title_sort_idx   ON products (lower(title), id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS products_updated_sort_idx ON products (updated_at, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS orders_total_sort_idx     ON orders   (total_minor, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS media_size_sort_idx       ON media    (size_bytes, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS variants_product_price_idx ON variants (product_id, price_minor);

CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_dead_idx ON outbox_events (id)
    WHERE dead AND published_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_name_idx ON outbox_events (event_name, id);
```

Run those against the live database before deploying, then the migration finds
them and does nothing. Copy the definitions exactly: `IF NOT EXISTS` matches on
the **name** only, so an index created under one of these names with a different
definition is accepted silently and then never used by the query it was meant
for. A store of a few thousand rows can ignore all of this — the indexes build
in milliseconds.

Shutdown is graceful. On SIGINT or SIGTERM the server stops accepting
connections, in-flight requests finish (up to 20 seconds), `OnStop` hooks run
in reverse order, and the database pool closes. `ListenAndServe` returns nil
after a clean shutdown, so any non-nil error is genuinely fatal.

### In a container

[`Dockerfile`](../Dockerfile) builds the panel from source, then a static binary,
into a distroless image that runs as a non-root user — one file, no libc, no
shell. [`docker-compose.yml`](../docker-compose.yml) adds PostgreSQL and a volume
for uploads, and starts the engine with every module this repository ships
except `-import-amazon` — each idle until it is configured. Trim its `command:`
to drop the ones a store will never use:

```sh
POSTGRES_PASSWORD=... GOCOMMERCE_ADMIN_TOKEN=... GOCOMMERCE_ADMIN_PASSWORD=...   docker compose up --build
```

Three things are worth knowing before it runs somewhere that matters:

- **`GOCOMMERCE_ADMIN_EMAIL` and `GOCOMMERCE_ADMIN_PASSWORD` create the first
  operator, and only while there is none.** Leaving them set does not reset a
  password on every restart, and it does not create a second operator.
- **The media volume is seeded from the image**, which is why `/data/media` is
  created in the build with the right ownership. Mount a volume there or uploads
  live only as long as the container.
- **There is no `HEALTHCHECK`**: the image has no shell to run one with. Point
  the proxy at `GET /health`, as below.

On a PaaS that builds from a Git repository — Dokploy, Coolify, Railway — point
it at the compose file and set the same variables in its own environment tab
rather than committing them.

The engine sees only the variables the compose file lists under its
`environment:`, so a key set in the host and missing from that list never
reaches it. Email and SMS are listed, each optional, and are every store's
default until a store enters its own:

| Variable | What it does |
|---|---|
| `RESEND_API_KEY` | Sends email through Resend. With nothing else set it sends from Resend's onboarding address. |
| `DEFAULT_EMAIL_FROM_ADDRESS` | The sender for Resend and SendGrid when their own `RESEND_FROM` / `SENDGRID_FROM` is empty. It must be on a domain the provider has verified. |
| `SENDGRID_API_KEY` | Sends email through SendGrid. |
| `TWILIO_ACCOUNT_SID`, `TWILIO_AUTH_TOKEN`, `TWILIO_FROM` | Sends texts through Twilio. |
| `MSG91_AUTH_KEY` (or `MSG91_AUTHKEY`) | Sends texts through MSG91. |
| `SMS_OTP_TEMPLATE_ID` (the sign-in code), `MSG91_TEMPLATE_ORDER_CODE`, `MSG91_TEMPLATE_ORDER_PLACED`, `MSG91_TEMPLATE_PAYMENT_RECEIVED`, `MSG91_TEMPLATE_ORDER_SHIPPED`, `MSG91_TEMPLATE_ORDER_DELIVERED`, `MSG91_TEMPLATE_ORDER_CANCELLED`, `MSG91_TEMPLATE_REFUND_ISSUED` | The DLT template id MSG91 sends for each message, for every store that has not set its own under Notifications › Setup SMS. MSG91 texts nothing it has no template for; a code's template carries the code as `##otp##` (or `##code##`), and may use the store's name as `##website##` (or `##store_name##`) and `##expires_in_minutes##`. These are MSG91's own template ids, not the DLT template ids. The sign-in code's must be an OTP template: it is sent through MSG91's OTP API, which is the only way MSG91 delivers one. |
| `SMS_SENDER_ID` | The DLT-registered sender id MSG91 sends every text from, such as `MISIKI`. Empty uses the one set on each template in MSG91. |

Sign-up, password-reset links and the stores' own addresses are listed too:

| Variable | What it does |
|---|---|
| `GOCOMMERCE_PANEL_URL` | Where the panel is reached, e.g. `https://shop.example.com`. A password-reset email links there; without it the email carries only a code. |
| `GOCOMMERCE_SIGNUP` | `1` lets anybody make an account on the sign-in screen and open a store. It stays shut until the install has email of its own: `RESEND_API_KEY` or `SENDGRID_API_KEY` here, not a store's own email settings. |
| `GOCOMMERCE_BASE_DOMAIN` | Gives every store an address at `<slug>.<base domain>` without adding a domain, e.g. `shops.example.com`. The panel shows it, and a domain under it cannot be added as a store's own. Point a wildcard DNS record at whatever serves the storefront. |
| `GOCOMMERCE_BRANDS` | Several brands on one install, each a name on its own admin domain with its own stores. A JSON array; see [Several brands](#several-brands). Unset, the install is one brand made of the two settings above. |
| `GOCOMMERCE_SECRETS_KEY`, `GOCOMMERCE_SECRETS_KEY_PREVIOUS` | Seal the payment and provider keys, API key copies and webhook signing secrets the database holds. Empty by default, which stores them readable as before. See [Secrets at rest](#secrets-at-rest) before setting it. |

Notifications › Setup Email has a **Send test email** button: it sends one
message now and shows the provider's answer, such as an unverified sender.

### A store's own domain, without touching the proxy

A store adds its own domain under Settings › Domains, adds the TXT record the
panel shows, and presses Verify. From then on the engine sends that domain's
requests to the store, but nothing reaches the engine until the reverse proxy
has a route and a certificate for the name. Behind Traefik, as on Dokploy, the
engine can give it both: Traefik's HTTP provider polls the engine, which
answers with a router per verified domain, and Traefik asks Let's Encrypt for
each certificate itself. A domain is live within a poll of being verified and
gone within a poll of being removed. Only verified domains are listed, so
somebody pointing their own DNS at the server cannot spend the install's
certificate allowance.

| Variable | What it does |
|---|---|
| `GOCOMMERCE_DOMAIN_TARGET` | Where a store's domain should point: this server's IP address, which the panel shows as an `A` (or `AAAA`) record, or a host name, shown as a `CNAME`. An apex domain such as `example.com` cannot be a `CNAME`, so the IP address is the safer choice. |
| `GOCOMMERCE_TRAEFIK_TOKEN` | A long random string, e.g. `openssl rand -hex 32`. It turns on `GET /internal/traefik` and is the bearer token Traefik must send; unset, the route answers 404. The admin token does not open it. |
| `GOCOMMERCE_TRAEFIK_UPSTREAM` | Where a store domain's visitors go: a URL Traefik can reach, such as `http://gocommerce:8080`, or the name of a service another provider defines, such as `storefront@docker`. Required with the token. |
| `GOCOMMERCE_TRAEFIK_CERT_RESOLVER` | The ACME resolver in Traefik's static configuration. Defaults to `letsencrypt`, Dokploy's. |

Then tell Traefik where to look, once, in its static configuration (on
Dokploy: Settings › Traefik › `traefik.yml`), and restart Traefik:

```yaml
providers:
  http:
    endpoint: "http://gocommerce:8080/internal/traefik"
    pollInterval: "15s"
    headers:
      Authorization: "Bearer <GOCOMMERCE_TRAEFIK_TOKEN>"
```

The routers use the `web` and `websecure` entry points, which is what Dokploy
names ports 80 and 443; plain http redirects to https. If a poll fails,
Traefik keeps the routes it last had, and the engine answers a failed database
read with an error rather than an empty list for the same reason. A domain
that a Dokploy domain entry already routes keeps working: both routes send it
to the same place.

### Addresses a store sets

Some addresses a store operator types are ones the server then connects to: a
webhook endpoint, a payment or carrier module's API base URL, a store's own
Meilisearch host, IndexNow's storefront URL, a Shopify shop to import from.
The server calls those only when they are public — not loopback, a private
range, link-local or another address that is not on the public internet — and
it checks the address it actually connects to, after DNS and on every
redirect. A new webhook endpoint must also be `https`. An address that breaks
the rule is refused when it is saved, and a request to one that got in before
this rule fails like any other unreachable endpoint, with the reason in the
delivery log or the provider's error.

An install where everyone who sets those addresses also runs the server — one
business's own store, or a development machine — can lift the rule:

| Variable | What it does |
|---|---|
| `GOCOMMERCE_ALLOW_PRIVATE_URLS` | `1` lets the addresses a store sets be private or loopback ones, and new webhook endpoints be plain `http`. Same as `-allow-private-urls`. `gocommerce doctor` warns while it is on. |

It is not in the compose file on purpose. That file is the shape an install
hosting stores for other people runs, and there the switch must stay off; a
single-business install that wants it adds the line itself.

While the rule is on, those requests do not go through `HTTP_PROXY` or
`HTTPS_PROXY`: through a proxy the server cannot see where a request really
goes. The install's own addresses — `MEILI_HOST`, `IMPORT_AMAZON_LLM_URL` —
are set by whoever runs the server and are not held to it.

### Several brands

One install — one database, one deployment — can answer under several names,
each on its own admin domain (D114). The panel at `admin.alpha.example` is
called Alpha and the one at `admin.beta.example` is called Beta; everything
behind them is the same engine. List the brands in `GOCOMMERCE_BRANDS`:

```json
[
  {"name": "Alpha", "panel_url": "https://admin.alpha.example", "base_domain": "alpha.example", "logo_url": ""},
  {"name": "Beta",  "panel_url": "https://admin.beta.example",  "base_domain": "beta.example",
   "logo_url": "https://cdn.beta.example/logo.svg"},
  {"name": "Gamma", "panel_url": "https://admin.gamma.example", "base_domain": "gamma.example",
   "alias_base_domains": ["shops.gamma.example"], "reserved": ["www", "status", "updates"]}
]
```

| Field | What it does |
|---|---|
| `name` | What the panel and the emails to staff call the product. Required. |
| `panel_url` | Where this brand's panel is reached: `https://` and a host, no path (plain `http` only with `-dev`). Required, and no two brands share a host. |
| `base_domain` | This brand's stores answer at `<slug>.<base_domain>`, and the panel shows that address. Optional. |
| `alias_base_domains` | Older base domains this brand's stores still answer under, so addresses given out before keep working. Nothing the engine shows or sends uses them. |
| `reserved` | More names no store may take, beside the platform's own (`www`, `admin`, `api`, `mail` and the like) and the first label of every brand's panel host, which are always kept. A brand whose stores live on its own apex lists here every other host it uses under that apex. |
| `logo_url` | A picture the panel shows where its mark is, `https://`. Empty keeps the mark. One that cannot be used is dropped with a warning at start and in `doctor`, and the mark is shown. |

The engine refuses to start, naming the entry, when a brand has no name or no
panel URL, a panel URL is not `https`, two brands share a panel host, or a base
domain or alias is used twice. **That stops the whole install, every brand
with it**, so check a new value before deploying it: `gocommerce doctor` run
with the new `GOCOMMERCE_BRANDS` (and `-dev` only where `serve` has it)
reports what would stop the engine as a failed `brands` check rather than
refusing to run. `gocommerce doctor` also lists the brands, the store domains
each answers under, and how many stores each has.

**Each store belongs to one brand.** It answers at `<slug>.<its brand's base
domain>` (and its aliases), and under another brand's base domain it is
nobody's, as an unknown store is. A store's own domains (Settings › Domains)
are unchanged and still win, and on a store's own domain the panel and its
emails are the store's brand. A store opened on a brand's admin domain belongs
to that brand. Store addresses stay unique across the whole install, so two
brands cannot both have a store called `mystore`.

**Which brand an address is**: a brand's panel host is that brand; a host
under a brand's base domain or alias is that brand; a store's own domain is
its store's brand; anything else is the **default brand** — the one whose
`base_domain` or alias is `GOCOMMERCE_BASE_DOMAIN`, else the first in the
list. Work no request started (a job, a command) is the default brand's too.

**Accounts are the install's.** One person has one password and can sign in at
any brand's admin domain, but sees and enters only that brand's stores there;
someone who opened or joined stores under two brands finds each brand's on its
own admin domain. A store of another brand is answered as one they are not on,
at sign-in too. The install's admin token (`GOCOMMERCE_ADMIN_TOKEN`) reaches
every store.

**Stores made before brands** are given one the first time the engine starts
with `GOCOMMERCE_BRANDS`: the default brand, which is where those stores
already answered. It is written once, so reordering the list later moves
nothing. The install's owner moves a store to another brand with

```sh
gocommerce store brand <slug> <panel host>    # e.g. gocommerce store brand mystore admin.beta.example
```

which moves its address to the new brand's base domain and is recorded in the
store's audit trail. The people on its team keep the brand they arrived
through; only the store moves.

**Changing a brand's panel address** needs one more step. Stores and people
record their brand by its panel host, so after changing a brand's `panel_url`
run

```sh
gocommerce brands rename-host <old host> <new host>   # e.g. admin.beta.example panel.beta.example
```

which moves every store and person still recorded under the old host to the
new one in one transaction, with a row in each store's audit trail. Until it
is run, the engine logs an error at start naming the old host and how many
stores it holds, `doctor` fails, and those stores are shown under the default
brand.

**The panel wears the brand.** The sign-in screens, the sidebar and every page
title say the name of the brand whose admin domain the panel is opened on, and
a brand's `logo_url` takes the mark's place.

**Emails to staff carry the brand.** A store's emails to its team — an
invitation, a reset sent from Settings › Team — name the store's brand and link
to its panel. A password reset asked for on a brand's admin domain is in that
brand only when the person belongs to it — a store of theirs is in it, or
they arrived through it — so a stranger cannot choose whose name a real
person's email carries; otherwise it goes out in the brand they arrived
through, or that of their first store. The link is only ever a configured
brand's panel URL, never the address the request came to. With no request
behind it, an email goes out the same way. The default wording says the
brand's name (`brand_name`) where it said GoCommerce. Emails to shoppers are
the store's own and do not change. An invitation is emailed only with a link
built from configuration; on an install with no panel address at all, the
owner is given the link to copy instead.

`GOCOMMERCE_PANEL_URL` and `GOCOMMERCE_BASE_DOMAIN` may stay set beside
`GOCOMMERCE_BRANDS` only if they name one of the brands (the base domain may be
an alias); the engine refuses to start otherwise, rather than leave the stores
under an old base domain unreachable. Unset, they take the first brand's
values, which is what a module reading them (cart recovery's tracked links)
gets.

**Hosting several brands** needs, for each brand:

- DNS: the panel host (`admin.beta.example`) and a wildcard for its store
  domains (`*.beta.example`, and `*.shops.gamma.example` for an alias)
  pointing at the reverse proxy.
- The reverse proxy: route the panel host and the wildcard hosts to the
  engine, passing the `Host` header through unchanged (Caddy and Traefik do by
  default) — the brand is read from it. A wildcard certificate needs the DNS
  challenge at most certificate authorities.
- A brand's panel host may also be one of a store's own domains. The panel
  there still knows its brand, and a request that names no store is that
  store's, as it was; `doctor` notes it.

### Running several instances

Nothing has to change. The outbox dispatcher claims rows with `FOR UPDATE SKIP
LOCKED`, so each process takes work no other process holds. The sweepers are
idempotent. There is no leader election to configure because there is no leader.
The one setting every instance must share is `GOCOMMERCE_SECRETS_KEY`, below.

## Secrets at rest

A few credentials have to be read back in full rather than checked against a
hash, so they cannot be hashed. With `GOCOMMERCE_SECRETS_KEY` set they are
sealed with AES-256-GCM before they reach the database, and a backup, a dump or
a replica holds ciphertext. Without it they are stored readable, as they always
were, and `gocommerce doctor` warns with the count.

| What | Where it is stored | Sealed as |
|---|---|---|
| Plugin settings of kind `secret`: payment gateway, carrier, email, SMS, search and marketing keys, and the Shopify import token | `plugins.settings` | `plugins/<plugin>/<field>` |
| The copy of an API key the owner can read again (M46) | `api_keys.secret` | `api_keys/secret` |
| Webhook signing secrets | `webhook_endpoints.secret` | `webhooks/endpoint_secret` |

Passwords, sessions, reset codes and API keys themselves are not on the list:
they are stored as hashes, because the engine only ever compares them. Order
and cart tokens are not either: the engine finds a row by its token, which a
value sealed with a random nonce cannot be searched for.

**Turning it on.**

1. Make a key, 32 random bytes in base64:

   ```sh
   openssl rand -base64 32
   ```

   or in PowerShell:

   ```powershell
   $b = New-Object byte[] 32; [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b); [Convert]::ToBase64String($b)
   ```

2. Keep a copy away from the server and away from its database backups, in a
   password manager the owner controls. A key kept beside the database protects
   nothing, and **a lost key cannot be recovered: every sealed secret has to be
   entered again** — gateway and provider keys typed in on their screens, API
   keys revoked and made again, webhook secrets rotated.
3. Set `GOCOMMERCE_SECRETS_KEY` in the engine's environment (a PaaS's
   environment tab; `docker-compose.yml` already passes it through) and
   redeploy. Every instance needs the same key.
4. Check **Settings › Diagnostics**: the `secrets` check reads
   `N secret(s) sealed under key <id>`.

That is the whole of it. As the engine starts with a key it seals what is
already stored, in every store, suspended ones too, in the background so the
store serves at once. From then on, what is entered is sealed as it is saved.
The pass is safe to repeat and safe with several instances starting together:
each value is sealed once. Its log has a line for each store it sealed
anything in, with counts per kind and never a value, and one line for the
whole pass, `stored secrets checked at start-up`. Under a database login that
passes row security it seals nothing and logs one warning saying why, as the
command below refuses to run.

`gocommerce secrets seal`, run with the module flags the store is served with,
does the same pass on demand and prints what it did. A hosted image has no
shell to run it in, and it is not needed there; it is for an install that
wants the counts at a terminal, or a last pass during a rotation. If it says a
module is not installed in that run, run it again with that module's flag. Run
it only against an engine already restarted with the key: a process still
running without the key cannot open what it seals, and its plugins read as not
configured until it restarts.

**Rotating.** Set the new key as `GOCOMMERCE_SECRETS_KEY` and the old one as
`GOCOMMERCE_SECRETS_KEY_PREVIOUS` and redeploy: the start-up pass moves
everything the old key sealed under the new one. Once Settings › Diagnostics no
longer notes values under the previous key, unset
`GOCOMMERCE_SECRETS_KEY_PREVIOUS` and redeploy again. `gocommerce secrets seal`
can make that last pass by hand instead. Rotating changes the key, not the secrets:
if a gateway key may have leaked along with the old key, rotate it at the
gateway too.

**When a key goes missing.** A sealed value names the key that sealed it (the
first eight hex characters of the key's SHA-256) and is bound to its store and
its field, so it does not open under another key, in another store or in
another field. One that no key here opens reads as unset to the code that uses
it: its plugin shows as not configured, an API key's copy cannot be shown again
(the key still authenticates), and a webhook delivery fails with a message to
rotate the secret rather than going out unsigned. The panel still shows a
plugin secret like that as stored, with a note beside the field that this
install cannot read it, so saving the plugin's settings for another reason
keeps it. It is kept until a new value is typed in or the field is emptied, so
putting the right key back as `GOCOMMERCE_SECRETS_KEY_PREVIOUS` and
restarting recovers it, even after a redeploy that briefly ran without the
key. Doctor names each field; the log names it once an hour.

A key that does not decode to 32 bytes, or `GOCOMMERCE_SECRETS_KEY_PREVIOUS`
set without `GOCOMMERCE_SECRETS_KEY`, stops the engine at startup with the
variable named and its value never printed. The key is read from the
environment only, never from a flag, because a flag is visible to anyone who can
list the machine's processes.

## Health checks

| Endpoint | Meaning | Use for |
|---|---|---|
| `GET /health` | The process is up. Touches nothing. | Liveness |
| `GET /health/ready` | The database answers. | Readiness |
| `GET /api/admin/diagnostics` | Every check `gocommerce doctor` runs, as JSON. | An operator, or an agent |

Point liveness at `/health`, not `/health/ready`. A database hiccup should take
a process out of the load balancer, not restart it.

The third is not a probe. It needs the `store.operate` right, where the first
two are public and unauthenticated, and it answers **200 even when the report
says the store is unwell** — the report is the answer rather than the error, and
`ok: false` is where the verdict lives. It is also the panel's Settings →
Diagnostics screen. When a check fails because it could not run, the underlying
driver error is written to the store's log rather than into that response: it
names a host, a port and a user. `gocommerce doctor` prints it.

## Backups

The database is the business. Everything else can be rebuilt.

```sh
pg_dump --format=custom "$DATABASE_URL" > gocommerce-$(date +%F).dump
pg_restore --clean --if-exists --dbname "$DATABASE_URL" gocommerce-2026-08-28.dump
```

Test the restore. An untested backup is a hope, not a backup — restore into a
scratch database on a schedule and check that the last order is there.

For anything with real revenue, move to point-in-time recovery: continuous WAL
archiving, or a managed PostgreSQL that does it for you. The difference matters
in the case that actually happens — not "the server died" but "someone ran the
wrong `DELETE` an hour ago".

## What to watch

**Outbox depth.** The one metric specific to this engine:

```sql
SELECT count(*) FILTER (WHERE published_at IS NULL AND NOT dead) AS pending,
       count(*) FILTER (WHERE dead)                              AS dead
FROM outbox_events;
```

Pending should hover near zero. A rising number means a consumer is failing or
a vendor is down. Anything `dead` is an event nobody could deliver after twelve
attempts — read `last_error`, fix the cause, and requeue:

```
POST /api/admin/events/retry-dead?name=order.paid
```

That route runs exactly the statement an operator used to type —
`UPDATE outbox_events SET dead = false, attempts = 0, available_at = now()
WHERE dead AND published_at IS NULL AND event_name = 'order.paid'` — and there
are two reasons to prefer it: it is logged with the operator's name, and it
cannot be typed without its `WHERE`. It answers `{"requeued": n, "remaining":
m}`, capped at 500 rows a call, so a non-zero `remaining` means call it again.
The panel draws the same thing at **Settings → Platform → Events**, where
`last_error` and the payload are readable a row at a time and a single event
can be retried on its own.

`GET /health/ready` covers the database. Beyond that, watch what you would for
any Go service: latency, error rate, connection-pool saturation.

**One request, every record it left.** Each request gets a trace id, returned
in the `X-Trace-Id` response header and printed on its `request` log line
beside the `store` it was for. A caller may send its own in the same header —
8 to 64 letters, digits, `-` or `_`; anything else is replaced — so a
storefront's support ticket can quote the id the shopper's browser saw. Any
failure the request logs carries the same `trace_id`. A caller's id is a
correlation hint, not an identity: two callers may send the same one, and
their records then share it. Modules read it with
`gocommerce.TraceID(ctx)` and keep it on what they record: the AI gateway's
jobs and runs carry it in `ai_jobs.trace_id` and `ai_executions.trace_id`.

**AI work waiting on a person.** With `-ai-gateway`, model work that cannot
finish on its own stops in `ai_jobs` as `failed` (refused, an answer that
failed its checks, an exhausted cap, a missing price) or `dead` (twelve
transient failures in a row), each with its `last_error`. The panel lists
them under **AI › Jobs**, with retry and cancel; the cap and what is spent
against it are under **AI › Usage**.

**A stock ledger that cannot explain the shelf.** `gocommerce doctor`'s
`stock ledger` check sums `stock_movements` per (variant, location) and
compares it with `variant_stock`. Inside the engine the two cannot drift — the
movement row is written in the same transaction as the balance it explains — so
a warning means something wrote `variant_stock` directly, against
[rule 3](../AGENTS.md). The balances are still the truth; what is missing is the
explanation. It warns rather than fails deliberately: the store is still
serving, and a check that failed forever over one hand-fix years ago is a check
operators learn to ignore. It is the production counterpart of
`TestTheLedgerReconcilesToTheShelf`.

**Reserved stock that never resolves.** The unpaid-order sweeper cancels
pending orders past `OrderTTL` and returns their inventory. If reserved
quantities climb anyway, look for orders stuck `pending` with a payment status
nobody ever settled.

**Carts.** The `carts` check reads `N live, M awaiting the sweeper, K
abandoned`. "Awaiting the sweeper" is expired and not yet dealt with, and it
warns past a thousand: either `Abandon` (which records the baskets with lines)
or `SweepExpired` (which deletes the empty ones) has stopped, and the number
climbs either way. Expect it once immediately after upgrading to M28, while the
backlog already in the table drains at 500 a pass. The second warning is
separate — abandoned carts past `CartRetention` that `PurgeAbandoned` has not
collected — because that is the bound which replaced deleting on expiry, and
the two fail independently.

## Housekeeping

The engine sweeps carts and unsettled orders — payment pending or recorded as
failed — every five minutes on its own. Carts are no longer a table that only
grows: an expired basket holding nothing is deleted, and one holding something
is marked `abandoned` and then deleted once `CartRetention` has passed.

Each pass is also reachable on demand, behind `store.operate`, from
Settings → Diagnostics or with curl:

```http
POST /api/admin/maintenance/sweep-carts
POST /api/admin/maintenance/sweep-unpaid
POST /api/admin/maintenance/drain-outbox
```

They run the same service methods the ticker runs, so pressing one does nothing
the store would not have done within five minutes. Every pass is bounded and
says so: a `capped: true` in the response means it filled its batch and there is
more, so press again. The outbox drain refuses with `409` while one is already
running in that process. **It will not touch a dead-lettered row** — the claim
filters `NOT dead`, so `POST /api/admin/events/retry-dead` above, or a per-event
retry, remains the only remedy for those.

*Converted* carts have never been swept and still are not, so they are a fourth
candidate for the manual DELETEs below — decide deliberately rather than by
habit, because a converted cart is order evidence. Three tables grow forever and
are yours to prune:

```sql
-- Delivered events, once you no longer need the audit trail.
DELETE FROM outbox_events WHERE published_at < now() - interval '90 days';

-- Webhook idempotency records, well past any gateway's retry window.
DELETE FROM payments_stripe_events WHERE received_at < now() - interval '30 days';

-- Stock movements, roughly two rows per order line plus every operator
-- movement. Nothing in the engine deletes one.
DELETE FROM stock_movements WHERE created_at < now() - interval '2 years';
```

Keep them longer than you think you need. They are how you answer "did we
actually send that?" and "why does the count say 4?" three weeks later — and
pruning the ledger is the one thing that makes the `stock ledger` check warn on
purpose, so prune whole periods rather than individual rows, and expect it.

**How long a shopper’s address is held.** A cart now lives at most
`CartTTL + CartRetention` — sixty days on the defaults, against thirty before
M28 — and the email on it lives exactly that long. It is a number an operator
has to be able to answer for. There is deliberately no admin write route on a
cart, so the only erasure levers are time and the shopper’s own
`PUT /api/carts/<token>/email` with an empty string; a store facing a deletion
request for a basket that has not become an order has no supported lever beyond
those two, which is a known gap rather than an oversight.

## Data in and out

```sh
gocommerce -db "$DATABASE_URL" export products > products.csv
gocommerce -db "$DATABASE_URL" import products products.csv --dry-run
```

Or over the API, which is the same code:

```sh
curl -H "Authorization: Bearer $TOKEN" \
     "$STORE/api/admin/export/admin-products" > products.csv

curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: text/csv" \
     --data-binary @products.csv \
     "$STORE/api/admin/import/products?dry_run=1"
```

Always dry-run first: it validates the whole file and rolls back, reporting
what it would have done.

Importing historical orders from another platform fires **no** events unless
you pass `fire_events`. Importing five thousand orders should not send five
thousand confirmation emails to people who bought something last year.

Exports prefix cells beginning with `=`, `+`, `-` or `@` with an apostrophe,
because a spreadsheet executes those when the file is opened. Import strips
exactly one back, so the round trip is lossless.

## Windows

Development on Windows is first class — there is no cgo anywhere in the engine,
so `go build` works with no C toolchain. For production, prefer Linux;
if you do run Windows, install the binary as a service with NSSM or `sc.exe`
rather than leaving a console window open.

## Security

- Terminate TLS at the proxy. The engine speaks plain HTTP.
- Admin tokens are compared in constant time, and there is no window where a
  wrong token takes measurably longer than a right one.
- Guest order lookup needs the order's access token, and a wrong token returns
  "not found" rather than "forbidden", so the endpoint cannot be used to
  discover which order numbers exist.
- Webhook secrets are mandatory in every payment module. A webhook endpoint
  without signature verification is an endpoint that lets anyone mark orders
  paid.
- The addresses a store sets — outgoing webhook endpoints, provider API base
  URLs and the rest — are called only when they are public, unless
  `GOCOMMERCE_ALLOW_PRIVATE_URLS` is on (see [Addresses a store
  sets](#addresses-a-store-sets)). Leave it off on any install where people
  you do not run the server with can open a store.
- Set `GOCOMMERCE_SECRETS_KEY`, so the payment keys and other credentials the
  database holds are sealed rather than readable by whoever holds a backup
  ([Secrets at rest](#secrets-at-rest)).
- Set `Dev: false` in production. It exists to let the engine boot without an
  admin token, which is exactly what you do not want.
