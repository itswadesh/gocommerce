package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/misiki/gocommerce/core"
)

// builtinTools are the store operations an agent can perform.
//
// Each one calls a domain service — the same one the REST API calls — so an
// agent cannot reach a state a person could not, and cannot skip a rule by
// coming in through a different door.
//
// Each one also names the rights core names on the equivalent REST route, so
// an agent driven by a session operator cannot reach through this door what
// the front door refuses.
func (m *Module) builtinTools() []Tool {
	return []Tool{
		{
			Name: "store_info",
			Description: "Describe this store: its currency, languages and the " +
				"payment and fulfillment methods installed.",
			// No rights: it is the store's own shape, and every role keeps
			// catalog.read however it is re-cut, so there is nobody this
			// could be kept from who is signed in at all.
			Call: func(ctx context.Context, _ json.RawMessage) (any, error) {
				cfg := m.app.Config()
				return map[string]any{
					"currency":              cfg.Currency,
					"default_language":      cfg.DefaultLanguage,
					"languages":             cfg.Languages,
					"payment_methods":       m.app.Pay().Methods(),
					"fulfillment_providers": m.app.Ship().Providers(),
					"engine_version":        gocommerce.Version,
				}, nil
			},
		},
		{
			Name: "store_health",
			Description: "Run operational diagnostics: database, migrations, admin " +
				"access, the event outbox, stock reservations, carts, catalog and " +
				"the API contract. Each check reports ok, warn or fail with a hint. " +
				"Call this first when something is behaving oddly.",
			// Diagnostics is what store.operate names, and the mount already
			// requires it — stated here anyway so the tool carries its own
			// answer rather than inheriting one from where it happens to be
			// mounted. ServeStdio has no mount at all.
			Rights: []gocommerce.Right{gocommerce.RightStoreOperate},
			Call: func(ctx context.Context, _ json.RawMessage) (any, error) {
				// The same report `gocommerce doctor` renders. An agent asked to
				// diagnose a store should not have to infer health from a dozen
				// separate reads, and must never reach for SQL to do it.
				return m.app.Diagnose(ctx), nil
			},
		},
		{
			Name:        "list_products",
			Description: "List products in the catalog, optionally filtered by a search term.",
			Rights:      []gocommerce.Right{gocommerce.RightCatalogRead},
			InputSchema: object(props{
				"query":  str("Match against title and description."),
				"status": enumStr("Filter by status.", "draft", "active", "archived"),
				"limit":  integer("How many to return (default 20, max 200)."),
			}),
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					Query  string `json:"query"`
					Status string `json:"status"`
					Limit  int    `json:"limit"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				products, total, err := m.app.Products().ListProducts(ctx, gocommerce.ProductQuery{
					Search: args.Query, Status: args.Status,
					Limit: limitOr(args.Limit, 20),
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"total": total, "products": summarizeProducts(products)}, nil
			},
		},
		{
			Name:        "get_product",
			Description: "Get one product with all of its variants and stock levels.",
			Rights:      []gocommerce.Right{gocommerce.RightCatalogRead},
			InputSchema: object(props{"id": integer("The product id.")}, "id"),
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					ID int64 `json:"id"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				return m.app.Products().GetProduct(ctx, args.ID)
			},
		},
		{
			Name: "list_low_stock_variants",
			Description: "List sellable variants at or below a stock threshold — " +
				"what needs reordering. The threshold is against the store's total " +
				"across every location: a variant with one unit in each of five shops " +
				"is not low by this reading, even though every shelf looks it. Pass " +
				"location_id to threshold against one place instead.",
			Rights: []gocommerce.Right{gocommerce.RightInventoryRead},
			InputSchema: object(props{
				"threshold":   integer("Available units at or below this count (default 5)."),
				"location_id": integer("Threshold against this location alone, rather than the store's total."),
				"limit":       integer("How many to return (default 50)."),
			}),
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					Threshold  *int  `json:"threshold"`
					LocationID int64 `json:"location_id"`
					Limit      int   `json:"limit"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				threshold := 5
				if args.Threshold != nil {
					threshold = *args.Threshold
				}
				var variants []*gocommerce.Variant
				var total int
				var err error
				if args.LocationID != 0 {
					variants, total, err = m.app.Stock().AtLocation(ctx, args.LocationID,
						gocommerce.LocationStockQuery{
							Threshold: &threshold, Order: gocommerce.StockOrderAvailable,
							Limit: limitOr(args.Limit, 50),
						})
				} else {
					variants, total, err = m.app.Stock().LowStock(ctx, gocommerce.LowStockQuery{
						Threshold: threshold, Limit: limitOr(args.Limit, 50),
					})
				}
				if err != nil {
					return nil, err
				}
				return map[string]any{"total": total, "variants": summarizeVariants(variants)}, nil
			},
		},
		{
			Name:        "list_orders",
			Description: "List orders, most recent first, optionally filtered.",
			Rights:      []gocommerce.Right{gocommerce.RightOrdersRead},
			InputSchema: object(props{
				"status":         enumStr("Order status.", "pending", "confirmed", "partial", "shipped", "delivered", "cancelled"),
				"payment_status": enumStr("Payment status.", "pending", "paid", "failed", "refunded"),
				"email":          str("Filter by customer email."),
				"limit":          integer("How many to return (default 20, max 200)."),
			}),
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					Status        string `json:"status"`
					PaymentStatus string `json:"payment_status"`
					Email         string `json:"email"`
					Limit         int    `json:"limit"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				orders, total, err := m.app.Order().List(ctx, gocommerce.OrderQuery{
					Status: args.Status, PaymentStatus: args.PaymentStatus,
					Email: args.Email, Limit: limitOr(args.Limit, 20),
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"total": total, "orders": summarizeOrders(orders)}, nil
			},
		},
		{
			Name:        "get_order",
			Description: "Get one order in full, including its lines and shipments.",
			Rights:      []gocommerce.Right{gocommerce.RightOrdersRead},
			InputSchema: object(props{"id": integer("The order id.")}, "id"),
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					ID int64 `json:"id"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				order, err := m.app.Order().Get(ctx, args.ID)
				if err != nil {
					return nil, err
				}
				// The access token is the customer's credential, not something
				// an agent needs in order to read the order.
				order.AccessToken = ""
				return order, nil
			},
		},
		{
			Name: "update_variant_inventory",
			Description: "Change a variant's stock. Use adjust to move it by a delta " +
				"(receiving stock) or set to replace it (a stock take). Stock cannot " +
				"go below what is already reserved for open orders.",
			Rights: []gocommerce.Right{gocommerce.RightInventoryWrite},
			InputSchema: object(props{
				"variant_id": integer("The variant id."),
				"adjust":     integer("Move the on-hand count by this much."),
				"set":        integer("Replace the on-hand count with this."),
				"reason": str("Why the stock moved — a delivery, a damage write-off, " +
					"a recount. Recorded in the stock ledger against this movement."),
			}, "variant_id"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					VariantID int64 `json:"variant_id"`
					Adjust    *int  `json:"adjust"`
					Set       *int  `json:"set"`
					// Omitted means the default location, which is the only
					// one most stores have and the one an agent that has not
					// been told about locations should be moving.
					LocationID int64 `json:"location_id"`
					// An agent that has to say why it moved stock is exactly
					// the actor whose reason an operator will most want to read
					// back off the ledger.
					Reason string `json:"reason"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				switch {
				case args.Adjust != nil && args.Set != nil:
					return nil, fmt.Errorf("send either adjust or set, not both")
				case args.Adjust != nil:
					return m.app.Stock().Adjust(ctx, args.VariantID, args.LocationID, *args.Adjust, args.Reason)
				case args.Set != nil:
					return m.app.Stock().SetOnHand(ctx, args.VariantID, args.LocationID, *args.Set, args.Reason)
				default:
					return nil, fmt.Errorf("send either adjust or set")
				}
			},
		},
		{
			Name: "mark_order_paid",
			Description: "Record that an order has been paid — how cash on delivery is " +
				"settled. This also confirms the order so it can be shipped.",
			Rights: []gocommerce.Right{gocommerce.RightOrdersWrite},
			InputSchema: object(props{
				"order_id":  integer("The order id."),
				"reference": str("The payment reference, if there is one."),
			}, "order_id"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					OrderID   int64  `json:"order_id"`
					Reference string `json:"reference"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				return m.app.Pay().MarkPaid(ctx, args.OrderID, args.Reference)
			},
		},
		{
			Name: "cancel_order",
			Description: "Cancel an order and return its stock. An order that has already " +
				"shipped cannot be cancelled — that is a return.",
			Rights: []gocommerce.Right{gocommerce.RightOrdersWrite},
			InputSchema: object(props{
				"order_id": integer("The order id."),
				"reason":   str("Why it is being cancelled."),
			}, "order_id"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					OrderID int64  `json:"order_id"`
					Reason  string `json:"reason"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				return m.app.Order().Cancel(ctx, args.OrderID, args.Reason)
			},
		},
		{
			Name:        "create_fulfillment",
			Description: "Ship a confirmed order, in whole or in part, recording a tracking number.",
			Rights:      []gocommerce.Right{gocommerce.RightOrdersFulfill},
			InputSchema: object(props{
				"order_id": integer("The order id."),
				"provider": str("Fulfillment provider code; defaults to manual."),
				"tracking": str("The tracking number, for manual fulfillment."),
				"carrier":  str("Who is carrying it, as a carrier code. Worked out from the tracking number when left out."),
				"lines":    shipLines("What goes in this parcel. Omit it to ship everything the order still owes."),
			}, "order_id"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					OrderID  int64                 `json:"order_id"`
					Provider string                `json:"provider"`
					Tracking string                `json:"tracking"`
					Carrier  string                `json:"carrier"`
					Lines    []gocommerce.ShipLine `json:"lines"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				return m.app.Ship().Create(ctx, args.OrderID, args.Provider,
					gocommerce.ShipRequest{Tracking: args.Tracking, Carrier: args.Carrier, Lines: args.Lines})
			},
		},
		{
			Name:        "mark_order_delivered",
			Description: "Record that a shipped order reached the customer.",
			// orders.write, not orders.fulfill: core puts delivery on
			// POST /api/admin/orders/{id}/deliver behind orders.write, and a
			// tool that disagreed with its own REST route would be a second
			// answer to one question.
			Rights:      []gocommerce.Right{gocommerce.RightOrdersWrite},
			InputSchema: object(props{"order_id": integer("The order id.")}, "order_id"),
			Mutates:     true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					OrderID int64 `json:"order_id"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				return m.app.Order().MarkDelivered(ctx, args.OrderID)
			},
		},

		// ---------------------------------------------------------- catalogue
		//
		// The tools above let an agent run a store that already exists. These
		// let it build one, which is the other half of the job an agent is
		// usually given: import a catalogue, fix a price, put a sale on.

		{
			Name: "create_product",
			Description: "Create a product. Give price_minor and stock to have it " +
				"created with a single default variant; omit them and the product " +
				"is created bare, for variants to be added afterwards.",
			Rights: []gocommerce.Right{gocommerce.RightCatalogWrite},
			InputSchema: object(props{
				"title":       str("What the product is called. Required."),
				"description": str("Longer copy for the product page."),
				"slug":        str("URL segment. Derived from the title when omitted."),
				"status":      enumStr("Publication state (default draft).", "draft", "active", "archived"),
				"vendor":      str("The manufacturer or brand, as a Google feed would send it."),
				"tags":        arrayOfStr("Free-text tags."),
				"sku":         str("SKU for the default variant. Generated from the title when omitted."),
				"price_minor": integer("Price of the default variant in minor units — 4500 is 45.00. Never a decimal."),
				"stock":       integer("Opening stock for the default variant."),
			}, "title"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					Title       string   `json:"title"`
					Description string   `json:"description"`
					Slug        string   `json:"slug"`
					Status      string   `json:"status"`
					Vendor      string   `json:"vendor"`
					Tags        []string `json:"tags"`
					SKU         string   `json:"sku"`
					PriceMinor  *int64   `json:"price_minor"`
					Stock       *int     `json:"stock"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				in := gocommerce.ProductInput{
					Title: args.Title, Description: args.Description, Slug: args.Slug,
					Status: args.Status, Vendor: args.Vendor, Tags: args.Tags, SKU: args.SKU,
				}
				if args.PriceMinor != nil {
					in.PriceMinor = args.PriceMinor
				}
				if args.Stock != nil {
					in.Stock = args.Stock
				}
				return m.app.Products().CreateProduct(ctx, in)
			},
		},
		{
			Name: "update_product",
			Description: "Change a product's copy, status or tags. Only the fields " +
				"given are touched; everything else is left as it is.",
			Rights: []gocommerce.Right{gocommerce.RightCatalogWrite},
			InputSchema: object(props{
				"product_id":  integer("The product id."),
				"title":       str("New title."),
				"description": str("New description."),
				"status":      enumStr("New publication state.", "draft", "active", "archived"),
				"vendor":      str("New vendor."),
				"tags":        arrayOfStr("Replacement tag list."),
			}, "product_id"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					ProductID   int64     `json:"product_id"`
					Title       *string   `json:"title"`
					Description *string   `json:"description"`
					Status      *string   `json:"status"`
					Vendor      *string   `json:"vendor"`
					Tags        *[]string `json:"tags"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				// Pointers all the way through: a patch that cannot tell "not
				// mentioned" from "set to empty" would let an agent blank a
				// description by talking about the title.
				return m.app.Products().UpdateProduct(ctx, args.ProductID, gocommerce.ProductPatch{
					Title: args.Title, Description: args.Description,
					Status: args.Status, Vendor: args.Vendor, Tags: args.Tags,
				})
			},
		},
		{
			Name: "set_variant_price",
			Description: "Set one variant's price, in minor units — 1999 is 19.99. " +
				"A decimal is refused rather than rounded.",
			Rights: []gocommerce.Right{gocommerce.RightCatalogWrite},
			InputSchema: object(props{
				"variant_id":  integer("The variant id."),
				"price_minor": integer("The new price in minor units. Must be a whole number."),
				"compare_at_price_minor": integer(
					"Optional was-price in minor units, for showing a discount. Send 0 to clear it."),
			}, "variant_id", "price_minor"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				// json.Number, not int64: encoding/json would accept 19.99 into
				// an int64 field as an error, but it would accept 1999.0 quietly
				// and that is a different price than the agent meant to send.
				var args struct {
					VariantID           int64        `json:"variant_id"`
					PriceMinor          json.Number  `json:"price_minor"`
					CompareAtPriceMinor *json.Number `json:"compare_at_price_minor"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				price, err := wholeMinor("price_minor", args.PriceMinor)
				if err != nil {
					return nil, err
				}
				patch := gocommerce.VariantPatch{PriceMinor: &price}
				if args.CompareAtPriceMinor != nil {
					compare, err := wholeMinor("compare_at_price_minor", *args.CompareAtPriceMinor)
					if err != nil {
						return nil, err
					}
					patch.CompareAtPriceMinor = gocommerce.SetAmount(compare)
				}
				return m.app.Products().UpdateVariant(ctx, args.VariantID, patch)
			},
		},
		{
			Name: "create_discount",
			Description: "Create a discount code. A percentage takes value_bp in " +
				"basis points — 1000 is 10%. A fixed amount takes value_minor.",
			Rights: []gocommerce.Right{gocommerce.RightDiscountsWrite},
			InputSchema: object(props{
				"code":        str("The code shoppers type. Required."),
				"title":       str("What it is for, shown to operators."),
				"kind":        enumStr("How the value is read (default percentage).", "percentage", "fixed_amount"),
				"value_bp":    integer("Percentage in basis points — 1000 is 10%."),
				"value_minor": integer("Fixed amount off, in minor units."),
				"scope":       str("What it applies to. Omit for the whole order."),
				"target_ids":  arrayOfInt("Products, collections or categories, according to scope."),
			}, "code"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					Code       string  `json:"code"`
					Title      string  `json:"title"`
					Kind       string  `json:"kind"`
					ValueBP    int     `json:"value_bp"`
					ValueMinor int64   `json:"value_minor"`
					Scope      string  `json:"scope"`
					TargetIDs  []int64 `json:"target_ids"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				kind := args.Kind
				if kind == "" {
					kind = "percentage"
				}
				return m.app.Discounts().Create(ctx, gocommerce.DiscountInput{
					Code: args.Code, Title: args.Title, Kind: kind,
					ValueBP: args.ValueBP, ValueMinor: args.ValueMinor,
					Scope: args.Scope, TargetIDs: args.TargetIDs,
				})
			},
		},
		{
			Name: "refund_order",
			Description: "Refund an order, fully or in part. Omit amount_minor to " +
				"refund everything not already refunded. The payment method must " +
				"be one that can refund — cash on delivery cannot.",
			// orders.refund is its own right in core precisely because moving
			// money back is not the same permission as editing an order.
			Rights: []gocommerce.Right{gocommerce.RightOrdersRefund},
			InputSchema: object(props{
				"order_id":     integer("The order id."),
				"amount_minor": integer("How much to refund, in minor units. Omit for everything outstanding."),
				"reason":       str("Why, recorded on the refund and carried on the event."),
			}, "order_id"),
			Mutates: true,
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					OrderID     int64  `json:"order_id"`
					AmountMinor int64  `json:"amount_minor"`
					Reason      string `json:"reason"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				// The acting operator is carried through so the refund is
				// attributed to whoever the agent is working as, rather than to
				// nobody. A static admin token has no superuser, and core
				// accepts nil for exactly that case.
				return m.app.Pay().Refund(ctx, args.OrderID, gocommerce.RefundRequest{
					AmountMinor: args.AmountMinor, Reason: args.Reason,
				}, gocommerce.SuperuserFrom(ctx))
			},
		},

		// ------------------------------------------------------------ reading
		//
		// An agent asked "who is worth writing to" or "how did last month go"
		// should have an answer that is not a hand-rolled SQL query.

		{
			Name: "list_customers",
			Description: "List the people who have ordered, most recent first, with " +
				"what they have spent.",
			Rights: []gocommerce.Right{gocommerce.RightCustomersRead},
			InputSchema: object(props{
				"query": str("Match against name and email."),
				"limit": integer("How many to return (default 20, max 200)."),
			}),
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				customers, total, err := m.app.Order().Customers(ctx, gocommerce.CustomerQuery{
					Search: args.Query, Limit: limitOr(args.Limit, 20),
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"total": total, "customers": customers}, nil
			},
		},
		{
			Name: "sales_report",
			Description: "What the store sold over a period, grouped by day, week or " +
				"month. Amounts are minor units in the store's settlement currency.",
			Rights: []gocommerce.Right{gocommerce.RightReportsRead},
			InputSchema: object(props{
				"group_by": enumStr("Bucket size (default day).", "day", "week", "month"),
			}),
			Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var args struct {
					GroupBy string `json:"group_by"`
				}
				if err := decode(raw, &args); err != nil {
					return nil, err
				}
				groupBy := args.GroupBy
				if groupBy == "" {
					groupBy = "day"
				}
				report, err := m.app.Reports().Sales(ctx, gocommerce.SalesQuery{GroupBy: groupBy})
				if err != nil {
					return nil, err
				}
				// The currency is stated rather than assumed: a bare number of
				// minor units is unreadable without knowing whether it has two
				// decimal places, three, or none.
				return map[string]any{
					"currency": m.app.Config().Currency,
					"group_by": groupBy,
					"report":   report,
				}, nil
			},
		},
	}
}

// wholeMinor reads a money argument that must be an integer number of minor
// units.
//
// Money is integer minor units plus a currency code everywhere in this engine,
// and an agent sending 19.99 where 1999 was meant is the likeliest way that
// rule gets broken from outside. Truncating would charge a different price than
// the agent asked for and say nothing; this refuses and explains.
func wholeMinor(field string, n json.Number) (int64, error) {
	v, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf(
			"%s must be a whole number of minor units — 19.99 is sent as 1999, not %s", field, n.String())
	}
	return v, nil
}

// ------------------------------------------------------------------ summaries

// The list tools return summaries rather than whole records. An agent reading
// fifty orders does not need every field of each, and a smaller payload is a
// cheaper and clearer one.

func summarizeProducts(products []*gocommerce.Product) []map[string]any {
	out := make([]map[string]any, 0, len(products))
	for _, p := range products {
		variants := make([]map[string]any, 0, len(p.Variants))
		for _, v := range p.Variants {
			variants = append(variants, map[string]any{
				"id": v.ID, "sku": v.SKU, "label": v.Label,
				"price_minor": v.Price.AmountMinor, "available": v.Available,
			})
		}
		out = append(out, map[string]any{
			"id": p.ID, "title": p.Title, "slug": p.Slug,
			"status": p.Status, "variants": variants,
		})
	}
	return out
}

func summarizeVariants(variants []*gocommerce.Variant) []map[string]any {
	out := make([]map[string]any, 0, len(variants))
	for _, v := range variants {
		row := map[string]any{
			"id": v.ID, "product_id": v.ProductID, "sku": v.SKU, "label": v.Label,
			"on_hand": v.StockOnHand, "reserved": v.StockReserved, "available": v.Available,
		}
		// Both numbers when a read is about one place, both labelled: the three
		// above stay the store-wide sums, because that is what they are on every
		// other read and an agent comparing two answers must not find the same
		// key meaning two things.
		if at := v.AtLocation; at != nil {
			row["at_location"] = map[string]any{
				"location_id": at.LocationID, "location_code": at.LocationCode,
				"on_hand": at.OnHand, "reserved": at.Reserved, "available": at.Available,
			}
		}
		out = append(out, row)
	}
	return out
}

func summarizeOrders(orders []*gocommerce.Order) []map[string]any {
	out := make([]map[string]any, 0, len(orders))
	for _, o := range orders {
		out = append(out, map[string]any{
			"id": o.ID, "number": o.Number, "status": o.Status,
			"payment_status": o.PaymentStatus, "payment_method": o.PaymentProvider,
			"total_minor": o.Total.AmountMinor,
			// Without this an agent sees a full total with no sign that part of
			// it went back: payment_status stays "paid" until all of it has (D36).
			"refunded_minor": o.Refunded.AmountMinor,
			"currency":       o.Currency,
			"email":          o.Email, "items": len(o.Lines),
			"created_at": o.CreatedAt,
		})
	}
	return out
}

// ------------------------------------------------------------ schema helpers

type props map[string]any

func object(p props, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": map[string]any(p)}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integer(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func arrayOfStr(description string) map[string]any {
	return map[string]any{
		"type": "array", "description": description,
		"items": map[string]any{"type": "string"},
	}
}

func arrayOfInt(description string) map[string]any {
	return map[string]any{
		"type": "array", "description": description,
		"items": map[string]any{"type": "integer"},
	}
}

func enumStr(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

// shipLines is the one array-of-object argument in the tool set, and it is
// spelled out here rather than given a general array helper nothing else would
// use.
func shipLines(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"order_line_id": integer("A line the order already has, as returned in line_items[].id."),
				"quantity":      integer("How many of that line go in this parcel."),
			},
			"required": []string{"order_line_id", "quantity"},
		},
	}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("could not read the arguments: %w", err)
	}
	return nil
}

func limitOr(requested, fallback int) int {
	if requested <= 0 {
		return fallback
	}
	if requested > gocommerce.MaxLimit {
		return gocommerce.MaxLimit
	}
	return requested
}
