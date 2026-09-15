package gocommerce

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Orders and customers in Shopify's layout. The orders file goes both ways —
// a store moving here brings its history in exactly this shape, and a file
// written here opens in the tools built for it — and the customers file
// only leaves, because a customer here is a reading of the orders and there
// is nothing to write one back into.

// shopifyOrderHeader is Shopify's orders export, column for column. Most of
// the tail is blank on the way out — this store has no risk level, no
// employee, no note attributes — and ignored on the way in; the columns are
// there so the file is the file a Shopify tool expects.
var shopifyOrderHeader = []string{
	"Name", "Email", "Financial Status", "Paid at", "Fulfillment Status", "Fulfilled at", "Accepts Marketing",
	"Currency", "Subtotal", "Shipping", "Taxes", "Total", "Discount Code", "Discount Amount", "Shipping Method",
	"Created at", "Lineitem quantity", "Lineitem name", "Lineitem price", "Lineitem compare at price",
	"Lineitem sku", "Lineitem requires shipping", "Lineitem taxable", "Lineitem fulfillment status",
	"Billing Name", "Billing Street", "Billing Address1", "Billing Address2", "Billing Company", "Billing City",
	"Billing Zip", "Billing Province", "Billing Country", "Billing Phone",
	"Shipping Name", "Shipping Street", "Shipping Address1", "Shipping Address2", "Shipping Company", "Shipping City",
	"Shipping Zip", "Shipping Province", "Shipping Country", "Shipping Phone",
	"Notes", "Note Attributes", "Cancelled at", "Payment Method", "Payment Reference", "Refunded Amount", "Vendor",
	"Outstanding Balance", "Employee", "Location", "Device ID", "Id", "Tags", "Risk Level", "Source", "Lineitem discount",
	"Tax 1 Name", "Tax 1 Value", "Tax 2 Name", "Tax 2 Value", "Tax 3 Name", "Tax 3 Value", "Tax 4 Name", "Tax 4 Value",
	"Tax 5 Name", "Tax 5 Value", "Phone", "Receipt Number", "Duties", "Billing Province Name", "Shipping Province Name",
	"Payment ID", "Payment Terms Name", "Next Payment Due At", "Payment References",
}

// shopifyCustomerHeader is Shopify's customers export, column for column.
var shopifyCustomerHeader = []string{
	"Customer ID", "First Name", "Last Name", "Email", "Accepts Email Marketing",
	"Default Address Company", "Default Address Address1", "Default Address Address2", "Default Address City",
	"Default Address Province Code", "Default Address Country Code", "Default Address Zip", "Default Address Phone",
	"Phone", "Accepts SMS Marketing", "Total Spent", "Total Orders", "Note", "Tax Exempt", "Tags",
}

// shopifyOrderColumns are the store's own order columns a Shopify row is
// translated into, so ImportOrders past the translation is one importer.
var shopifyOrderColumns = []string{
	"number", "email", "status", "payment_status", "payment_provider", "currency", "created_at",
	"phone", "name", "address_line1", "address_line2", "city", "state", "postal_code", "country",
	"subtotal_minor", "shipping_minor", "discount_minor", "total_minor",
	"sku", "title", "variant_label", "quantity", "unit_price_minor",
}

// shopifyTime is how Shopify writes a timestamp: "2024-05-01 09:58:12 +0000".
const shopifyTime = "2006-01-02 15:04:05 -0700"

func parseShopifyTime(s string) (time.Time, bool) {
	for _, layout := range []string{shopifyTime, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// detectOrderFormat reads the dialect off a header: Shopify's has a Name and
// line items, the store's own a number.
func detectOrderFormat(cols map[string]int) Format {
	if _, ok := cols["lineitem quantity"]; ok {
		if _, ok := cols["name"]; ok {
			return FormatShopify
		}
	}
	return FormatNative
}

// shopifyOrderReader turns Shopify's rows into the store's own, one at a
// time. The order's own columns are on its first line only; the group
// importer reads them from there, so a blank on a later line is nothing.
type shopifyOrderReader struct {
	cols     map[string]int
	out      map[string]int
	currency string
}

func newShopifyOrderReader(cols map[string]int, currency string) *shopifyOrderReader {
	return &shopifyOrderReader{cols: cols, out: indexColumns(shopifyOrderColumns), currency: currency}
}

func (s *shopifyOrderReader) row(line int, record []string) (csvRow, error) {
	in := csvRow{line: line, cols: s.cols, values: record}
	values := make([]string, len(shopifyOrderColumns))
	set := func(name, v string) { values[s.out[name]] = v }

	// "#1001" is Shopify's name for order 1001; the store keeps the number.
	set("number", strings.TrimPrefix(in.get("name"), "#"))
	set("email", in.get("email"))
	first := in.get("email") != "" || in.get("created at") != "" || in.get("financial status") != ""
	if first {
		currency := in.get("currency")
		if currency == "" {
			currency = s.currency
		}
		set("currency", currency)
		exp := currencyExponent(currency)

		switch {
		case in.get("cancelled at") != "":
			set("status", OrderCancelled)
		case strings.EqualFold(in.get("fulfillment status"), "fulfilled"):
			set("status", OrderShipped)
		case strings.EqualFold(in.get("fulfillment status"), "partial"):
			set("status", OrderPartial)
		default:
			set("status", OrderConfirmed)
		}
		switch strings.ToLower(in.get("financial status")) {
		case "paid", "partially_refunded":
			set("payment_status", PaymentPaid)
		case "refunded":
			set("payment_status", PaymentRefunded)
		case "voided":
			set("payment_status", PaymentFailed)
		default:
			set("payment_status", PaymentPending)
		}
		set("payment_provider", in.get("payment method"))
		if t, ok := parseShopifyTime(in.get("created at")); ok {
			set("created_at", t.UTC().Format(time.RFC3339))
		}
		// The shipping address, or the billing one when there is only that.
		side := "shipping"
		if in.get("shipping address1") == "" && in.get("billing address1") != "" {
			side = "billing"
		}
		set("name", firstNonEmpty(in.get(side+" name"), in.get("shipping name"), in.get("billing name")))
		set("phone", firstNonEmpty(in.get("phone"), in.get(side+" phone")))
		set("address_line1", in.get(side+" address1"))
		set("address_line2", in.get(side+" address2"))
		set("city", in.get(side+" city"))
		set("state", in.get(side+" province"))
		set("postal_code", in.get(side+" zip"))
		set("country", in.get(side+" country"))
		for _, m := range [][2]string{
			{"subtotal", "subtotal_minor"}, {"shipping", "shipping_minor"},
			{"discount amount", "discount_minor"}, {"total", "total_minor"},
		} {
			if raw := in.get(m[0]); raw != "" {
				minor, err := parseMoney(raw, exp)
				if err != nil {
					return csvRow{}, fmt.Errorf("%s: %v", m[0], err)
				}
				set(m[1], strconv.FormatInt(minor, 10))
			}
		}
	}

	set("sku", in.get("lineitem sku"))
	set("title", in.get("lineitem name"))
	set("quantity", in.get("lineitem quantity"))
	if raw := in.get("lineitem price"); raw != "" {
		// The currency is on the first line; a later line of the same order
		// has the same one, and the group importer only reads it from there.
		exp := currencyExponent(firstNonEmpty(in.get("currency"), s.currency))
		minor, err := parseMoney(raw, exp)
		if err != nil {
			return csvRow{}, fmt.Errorf("lineitem price: %v", err)
		}
		set("unit_price_minor", strconv.FormatInt(minor, 10))
	}
	return csvRow{line: line, cols: s.out, values: values}, nil
}

// exportOrderLine is one order line with its order beside it, as the export
// reads them, for either writer.
type exportOrderLine struct {
	id                                                 int64
	number, status, payStatus, provider, reference     string
	currency, email, phone, name, lang, shippingMethod string
	createdAt, updatedAt                               time.Time
	addr                                               Address
	subtotal, shipping, discount, tax, total, refunded int64
	discountCode                                       string
	sku, title, label                                  string
	qty                                                int
	unitPrice, lineTotal                               int64
}

// shopifyOrderRow renders one line in Shopify's layout: the order's own
// columns on its first line only, which is how Shopify writes them.
func shopifyOrderRow(l exportOrderLine, first bool, exp int) []string {
	row := make([]string, len(shopifyOrderHeader))
	set := func(name, v string) { row[shopifyOrderColumn[name]] = v }
	set("Name", "#"+l.number)
	set("Lineitem quantity", strconv.Itoa(l.qty))
	name := l.title
	if l.label != "" {
		name += " - " + l.label
	}
	set("Lineitem name", name)
	set("Lineitem price", formatMoney(l.unitPrice, exp))
	set("Lineitem sku", l.sku)
	set("Lineitem requires shipping", "true")
	set("Lineitem taxable", "true")
	fulfillment := "pending"
	if l.status == OrderShipped || l.status == OrderDelivered {
		fulfillment = "fulfilled"
	}
	set("Lineitem fulfillment status", fulfillment)
	set("Lineitem discount", formatMoney(0, exp))
	if !first {
		return row
	}

	set("Email", l.email)
	financial := "pending"
	switch l.payStatus {
	case PaymentPaid:
		financial = "paid"
		if l.refunded > 0 {
			financial = "partially_refunded"
		}
	case PaymentRefunded:
		financial = "refunded"
	case PaymentFailed:
		financial = "voided"
	}
	set("Financial Status", financial)
	switch l.status {
	case OrderShipped, OrderDelivered:
		set("Fulfillment Status", "fulfilled")
	case OrderPartial:
		set("Fulfillment Status", "partial")
	default:
		set("Fulfillment Status", "unfulfilled")
	}
	set("Accepts Marketing", "no")
	set("Currency", l.currency)
	set("Subtotal", formatMoney(l.subtotal, exp))
	set("Shipping", formatMoney(l.shipping, exp))
	set("Taxes", formatMoney(l.tax, exp))
	set("Total", formatMoney(l.total, exp))
	set("Discount Code", l.discountCode)
	set("Discount Amount", formatMoney(l.discount, exp))
	set("Shipping Method", l.shippingMethod)
	set("Created at", l.createdAt.UTC().Format(shopifyTime))
	street := strings.TrimSpace(l.addr.Line1 + " " + l.addr.Line2)
	for _, side := range []string{"Billing", "Shipping"} {
		set(side+" Name", firstNonEmpty(l.addr.Name, l.name))
		set(side+" Street", street)
		set(side+" Address1", l.addr.Line1)
		set(side+" Address2", l.addr.Line2)
		set(side+" City", l.addr.City)
		set(side+" Zip", l.addr.PostalCode)
		set(side+" Province", l.addr.State)
		set(side+" Country", l.addr.Country)
		set(side+" Phone", firstNonEmpty(l.addr.Phone, l.phone))
	}
	if l.status == OrderCancelled {
		set("Cancelled at", l.updatedAt.UTC().Format(shopifyTime))
	}
	set("Payment Method", l.provider)
	set("Payment Reference", l.reference)
	set("Refunded Amount", formatMoney(l.refunded, exp))
	outstanding := int64(0)
	if l.payStatus == PaymentPending {
		outstanding = l.total
	}
	set("Outstanding Balance", formatMoney(outstanding, exp))
	set("Id", strconv.FormatInt(l.id, 10))
	set("Source", "web")
	set("Phone", l.phone)
	return row
}

var shopifyOrderColumn = func() map[string]int {
	m := make(map[string]int, len(shopifyOrderHeader))
	for i, name := range shopifyOrderHeader {
		m[name] = i
	}
	return m
}()

// exportCustomer is one person as the customer reading has them.
type exportCustomer struct {
	email, name, phone string
	addr               Address
	orders             int
	spent              int64
	first, last        time.Time
}

// shopifyCustomerRow renders one person in Shopify's layout. The name is
// split at its last space, which is right for most and wrong for the rest
// in a way a spreadsheet can see.
func shopifyCustomerRow(c exportCustomer, exp int) []string {
	row := make([]string, len(shopifyCustomerHeader))
	set := func(name, v string) { row[shopifyCustomerColumn[name]] = v }
	first, last := c.name, ""
	if i := strings.LastIndex(strings.TrimSpace(c.name), " "); i > 0 {
		first, last = c.name[:i], strings.TrimSpace(c.name[i+1:])
	}
	set("First Name", first)
	set("Last Name", last)
	set("Email", c.email)
	set("Accepts Email Marketing", "no")
	set("Default Address Address1", c.addr.Line1)
	set("Default Address Address2", c.addr.Line2)
	set("Default Address City", c.addr.City)
	set("Default Address Province Code", c.addr.State)
	set("Default Address Country Code", c.addr.Country)
	set("Default Address Zip", c.addr.PostalCode)
	set("Default Address Phone", firstNonEmpty(c.addr.Phone, c.phone))
	set("Phone", c.phone)
	set("Accepts SMS Marketing", "no")
	set("Total Spent", formatMoney(c.spent, exp))
	set("Total Orders", strconv.Itoa(c.orders))
	set("Tax Exempt", "no")
	return row
}

var shopifyCustomerColumn = func() map[string]int {
	m := make(map[string]int, len(shopifyCustomerHeader))
	for i, name := range shopifyCustomerHeader {
		m[name] = i
	}
	return m
}()
