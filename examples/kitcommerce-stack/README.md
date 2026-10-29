# kitcommerce-stack

GoCommerce and Svelte Commerce together on one machine: a storefront you can
buy from and the admin panel where the order lands. It is an example to run and
test, not a production deployment. For that, see
[`docker-compose.yml`](../../docker-compose.yml) at the repository root and
[docs/operations.md](../../docs/operations.md).

| Service | What it is |
|---|---|
| `postgres` | PostgreSQL 17, on the `pgdata` volume |
| `engine-login` | Creates the engine's own database login, then exits |
| `migrate` | `gocommerce migrate`, then exits |
| `gocommerce` | The engine and its admin panel, built from this checkout |
| `seed` | Waits for the engine, adds its sample shop, then exits |
| `storefront` | Svelte Commerce at a pinned commit, with `@misiki/gocommerce-connector` 0.14.0 |
| `caddy` | The proxy, on port 8000 |

## Prerequisites

- Docker with Compose v2.24 or later. The storefront is built from its Git
  repository, so the first build needs network access and takes a few minutes.
- About 3 GB of free disk for the images.
- For `scripts/smoke.sh` on the host: `sh`, `curl` and `jq`. Without them, run
  the smoke test in a container instead (see below).

## Start it

```sh
cd examples/kitcommerce-stack
cp .env.example .env
```

Open `.env` and set `POSTGRES_PASSWORD`, `GOCOMMERCE_ADMIN_TOKEN` and
`GOCOMMERCE_ADMIN_PASSWORD`. Use a new random value for each, for example the
output of `openssl rand -hex 24`. Then build and start the stack:

```sh
docker compose up -d --build --wait
```

The command returns when every service is healthy or has finished. The `seed`
service adds the engine's sample shop: eight products with photographs,
categories, collections, discount codes and a few orders. It runs on every
start, and a second run adds nothing that is already there.

## Try it

Chrome, Edge and Firefox send every `*.localhost` name to your own machine.

1. Open <http://shop.localhost:8000/products/a5-notebook>.
2. Select **Add to bag**, then open the bag and change the quantity.
3. Select **Proceed to shipping** and enter any address. Nothing is delivered.
4. Select **Cash on delivery**, **Review order** and **Confirm order**. The
   confirmation page shows the order number.
5. Open <http://admin.localhost:8000> and sign in with
   `GOCOMMERCE_ADMIN_EMAIL` and `GOCOMMERCE_ADMIN_PASSWORD` from `.env`.
6. Select **Orders**. The new order is at the top.

## Smoke test

`scripts/smoke.sh` places one order the way the storefront does and checks each
step:

1. The storefront renders a seeded product, and the shop's origin refuses the
   admin API.
2. A cart line is added, then its quantity is changed.
3. A cash-on-delivery checkout places an order.
4. The order is read with the access token checkout returned. A wrong token
   reads nothing.
5. The admin API returns the same order. Its messages are recorded and none is
   sent.
6. The same checkout sent again with the same `Idempotency-Key` returns the
   same order, and the order count does not change.

With `curl` and `jq` on the host:

```sh
set -a; . ./.env; set +a
sh scripts/smoke.sh
```

Without them:

```sh
docker compose --profile smoke run --rm smoke
```

The script prints `PASS` and exits 0, or prints the failed step and exits 1.
CI runs it on every pull request that changes this directory
(`.github/workflows/kitcommerce-stack.yml`).

## Start again from nothing

```sh
docker compose down -v
docker compose up -d --wait
```

`down -v` deletes the database and media volumes. Add `--build` to `up` after
you change the engine's code or `STOREFRONT_COMMIT`.

## How the origins are laid out

| Address | Goes to |
|---|---|
| `http://shop.localhost:8000/` | The storefront |
| `http://shop.localhost:8000/backend/api/...`, `/backend/x/...`, pictures | The engine's public API |
| `http://shop.localhost:8000/backend/api/admin/...` | 404 |
| `http://admin.localhost:8000/` | The admin panel and the whole API |

The panel is served at `/` of the engine, so it needs a name of its own. The
storefront can't share it.

The connector uses two settings:

- `PUBLIC_GOCOMMERCE_API_URL` is `http://gocommerce:8080`, the engine on the
  compose network. Connector 0.14.0 reads it only on the storefront's server,
  for server-side rendering.
- `PUBLIC_GOCOMMERCE_PROXY_PATH` is `/backend`. The connector in the shopper's
  browser calls `/backend/...` on the shop's own origin, and pictures load from
  there too. Caddy forwards those paths to the engine. Because the browser never
  calls another origin, `GOCOMMERCE_CORS_ORIGINS` stays unset.

The admin token is used only by `seed` and the smoke test. No page the browser
loads contains it.

## What the stack never does

- **Take a payment.** No gateway module is installed. Cash on delivery is the
  only payment method.
- **Send an email or a text.** No email or SMS module is installed. The engine
  records each message it would send, with the status `logged`, under
  **Notifications** in the panel.
- **Call an address a store typed.** `GOCOMMERCE_ALLOW_PRIVATE_URLS` is unset.

## Troubleshooting

**`required variable POSTGRES_PASSWORD is missing a value`.** A secret in `.env`
is empty. Compose names the one that is missing.

**The `migrate` service exits with an error.** Read its log with
`docker compose logs migrate`. The engine and everything after it wait for a
successful migration and do not start. If the volume holds a database from a
newer engine, start again from nothing.

**The storefront build fails.** The build fetches
`github.com/itswadesh/svelte-commerce` at `STOREFRONT_COMMIT` and installs from
its `bun.lock`. A failure at the `test ... = "$CONNECTOR_VERSION"` step means that
commit's lockfile has a different connector than `compose.yaml` expects. Change
`CONNECTOR_VERSION` and the commit together.

**The shop shows errors from `/backend`, or the browser reports CORS errors.**
The storefront reached the engine on a different origin. Keep
`PUBLIC_GOCOMMERCE_PROXY_PATH` set, and open the shop at the address in
`SHOP_HOST`. To serve the engine on its own origin instead, set
`GOCOMMERCE_CORS_ORIGINS` on the engine to the shop's origin.

**`shop.localhost` does not resolve.** Some browsers and tools don't resolve
`*.localhost`. Add `127.0.0.1 shop.localhost admin.localhost` to your hosts
file, or set `SHOP_HOST` and `ADMIN_HOST` to names you control.

**Port 8000 is in use.** Set `HTTP_PORT` in `.env`, and pass the new address to
the smoke test as `SHOP_URL` and `ADMIN_URL`.

## Known limitations

These are gaps in the integration today, not settings.

- **No shopper accounts.** The engine runs without `-identity`, so there is no
  sign-in, address book or order history. The storefront still shows its
  account icon and a **log in** link at checkout. Neither can sign anyone in,
  because the engine has no `/x/identity` routes.
- **No search index.** Search answers from the database. There is no
  typo-tolerant search and no price facets.
- **No contact form, reviews, wishlists, menus, pages or layouts.** Their
  modules are not installed. The contact page says the store takes no messages
  through the site. Each product page logs a 404 for
  `/backend/x/layouts/product` in the browser console.
- **Connector 0.14.0 only.** The storefront commit pins it. Payment by gateway,
  tax in the bag and facets have the gaps listed in the
  [connector's README](https://github.com/misiki-in/gocommerce-connector#where-the-two-do-not-meet-yet).
- **The storefront calls api.ipify.org.** `@misiki/kitcommerce-core` 0.1.33
  asks it for the shopper's IP address on every page load, from the browser.
  It is outside this repository.
