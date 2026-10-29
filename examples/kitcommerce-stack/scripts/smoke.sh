#!/bin/sh
# Walks a running kitcommerce-stack through one sale, the way a shopper's
# browser and an operator reach it, and exits non-zero at the first step that
# does not hold.
#
#   GOCOMMERCE_ADMIN_TOKEN=... sh scripts/smoke.sh
#   docker compose --profile smoke run --rm smoke     # no curl or jq needed
#
# Needs curl and jq. Each run places one new cash-on-delivery order.

set -eu

SHOP_URL=${SHOP_URL:-http://shop.localhost:8000}
ADMIN_URL=${ADMIN_URL:-http://admin.localhost:8000}
ADMIN_AUTH="Authorization: Bearer ${GOCOMMERCE_ADMIN_TOKEN:?set GOCOMMERCE_ADMIN_TOKEN}"
# The engine as the shopper's browser reaches it: the shop's own origin.
API=$SHOP_URL/backend
# A product of the engine's sample shop, which the seed service adds.
SLUG=${SMOKE_PRODUCT:-a5-notebook}

body=$(mktemp)
trap 'rm -f "$body"' EXIT

fail() {
	echo "FAIL  $*" >&2
	exit 1
}

expect() {
	[ "$2" = "$3" ] || fail "$1: want '$2', got '$3'"
	echo "ok    $1"
}

# req METHOD URL [curl args...] leaves the response in $body and its status in
# $status.
req() {
	method=$1 url=$2
	shift 2
	# curl answers every *.localhost name with this machine itself, so inside
	# the compose network each request is sent to the proxy by name instead.
	if [ -n "${SMOKE_CONNECT_TO:-}" ]; then
		set -- --connect-to "::$SMOKE_CONNECT_TO" "$@"
	fi
	status=$(curl -sS -o "$body" -w '%{http_code}' -X "$method" "$@" "$url") || fail "$method $url: no answer"
}

# json METHOD URL [curl args...] is req that fails on anything but a 2xx.
json() {
	req "$@" -H 'Content-Type: application/json'
	case $status in
	2??) ;;
	*) fail "$method $url answered $status: $(cat "$body")" ;;
	esac
}

# field [jq args...] FILTER reads one value from $body.
field() { jq -er "$@" "$body" || fail "no $* in: $(cat "$body")"; }

echo "Smoke test: shop $SHOP_URL, admin $ADMIN_URL"

echo '1. The storefront renders a seeded product'
json GET "$API/api/products/slug/$SLUG"
title=$(field .data.title)
variant=$(field '.data.variants[0].id')
price=$(field '.data.variants[0].price.amount_minor')
currency=$(field '.data.variants[0].price.currency')
req GET "$SHOP_URL/products/$SLUG"
expect 'the product page answers 200' 200 "$status"
grep -qF "$title" "$body" || fail "the product page does not show '$title'"
echo "ok    the product page shows '$title'"
req GET "$API/api/admin/orders" -H "$ADMIN_AUTH"
expect 'the admin API is not served on the shop origin' 404 "$status"
req GET "$API"
expect 'nor is the admin panel' 404 "$status"

echo '2. A cart line is added and updated'
json POST "$API/api/carts"
cart=$(field .data.id)
json POST "$API/api/carts/$cart/line-items" -d "{\"variant_id\": $variant, \"quantity\": 1}"
expect 'one item in the cart' 1 "$(field .data.item_count)"
line=$(field '.data.line_items[0].id')
json PATCH "$API/api/carts/$cart/line-items/$line" -d '{"quantity": 2}'
expect 'two items after the update' 2 "$(field .data.item_count)"
expect 'the subtotal is two of them' $((price * 2)) "$(field .data.subtotal.amount_minor)"

echo '3. Cash on delivery places an order'
json GET "$API/api/checkout"
field '.data.payment_methods | index("cod") != null' >/dev/null || fail "cash on delivery is not offered: $(cat "$body")"
echo 'ok    cash on delivery is offered'
key=smoke-$(date +%s)-$$
order_body=$(jq -nc --arg cart "$cart" '{
	cart_id: $cart, email: "smoke@example.com", name: "Smoke Test",
	address: {line1: "1 Test Street", city: "Testville", postal_code: "12345", country: "US"}
}')
json POST "$API/api/checkout/cod" -d "$order_body" -H "Idempotency-Key: $key"
expect 'nothing to pay now' none "$(field .data.payment.kind)"
expect 'the order is confirmed' confirmed "$(field .data.order.status)"
expect 'the order is in the store currency' "$currency" "$(field .data.order.total.currency)"
order_id=$(field .data.order.id)
number=$(field .data.order.number)
access=$(field .data.order.access_token)
echo "ok    order $number placed"

echo '4. The order is read back with its access token'
json GET "$API/api/orders/$number?token=$access"
expect 'the same order' "$number" "$(field .data.number)"
req GET "$API/api/orders/$number?token=not-the-token"
expect 'a wrong token reads nothing' 404 "$status"

echo '5. The order appears through the admin API'
json GET "$ADMIN_URL/api/admin/orders/$order_id" -H "$ADMIN_AUTH"
expect 'the admin sees the same order' "$number" "$(field .data.number)"
expect 'with its two units' 2 "$(field '[.data.line_items[].quantity] | add')"
# The engine records the order's messages. With no email or SMS module
# installed it has nothing to send them with, so each stays "logged".
json GET "$ADMIN_URL/api/admin/notifications?limit=50" -H "$ADMIN_AUTH"
expect 'its messages were recorded and none was sent' logged \
	"$(field --arg n "$number" '[.data[] | select(.order_number == $n) | .status] | unique | join(",")')"
json GET "$ADMIN_URL/api/admin/orders?limit=1" -H "$ADMIN_AUTH"
orders=$(field .meta.total)

echo '6. A repeated Idempotency-Key places no second order'
json POST "$API/api/checkout/cod" -d "$order_body" -H "Idempotency-Key: $key"
expect 'the replay answers the same order' "$order_id" "$(field .data.order.id)"
json GET "$ADMIN_URL/api/admin/orders?limit=1" -H "$ADMIN_AUTH"
expect 'the admin order count is unchanged' "$orders" "$(field .meta.total)"

echo "PASS  order $number"
