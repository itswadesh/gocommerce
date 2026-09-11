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
| `DefaultLanguage` / `Languages` | `en` / `["en"]` | Negotiated per request. |
| `AdminTokens` | — | At least one, unless `Dev`. Several allow rotation. |
| `AdminAuth` | bearer tokens | Replace to add sessions, OIDC or RBAC. |
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

**One migration is worth planning for on a large store.** M29 creates five
indexes on `products`, `orders`, `media` and `variants` so the admin listings
can be ordered by title, price, stock, size and total. Every migration runs
inside a transaction, so `CREATE INDEX CONCURRENTLY` is not available to it and
a plain `CREATE INDEX` holds a SHARE lock while it builds — on a large `orders`
table that is blocked checkouts for the length of the boot.

Every statement in it is `IF NOT EXISTS`, which is what lets a store that big
build them out of band first:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS products_title_sort_idx   ON products (lower(title), id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS products_updated_sort_idx ON products (updated_at, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS orders_total_sort_idx     ON orders   (total_minor, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS media_size_sort_idx       ON media    (size_bytes, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS variants_product_price_idx ON variants (product_id, price_minor);
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
for uploads:

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

### Running several instances

Nothing has to change. The outbox dispatcher claims rows with `FOR UPDATE SKIP
LOCKED`, so each process takes work no other process holds. The sweepers are
idempotent. There is no leader election to configure because there is no leader.

## Health checks

| Endpoint | Meaning | Use for |
|---|---|---|
| `GET /health` | The process is up. Touches nothing. | Liveness |
| `GET /health/ready` | The database answers. | Readiness |

Point liveness at `/health`, not `/health/ready`. A database hiccup should take
a process out of the load balancer, not restart it.

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

```sql
UPDATE outbox_events
SET dead = false, attempts = 0, available_at = now()
WHERE dead AND event_name = 'order.paid';
```

`GET /health/ready` covers the database. Beyond that, watch what you would for
any Go service: latency, error rate, connection-pool saturation.

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
- Set `Dev: false` in production. It exists to let the engine boot without an
  admin token, which is exactly what you do not want.
