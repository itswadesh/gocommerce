// Package shiprocket books shipments through Shiprocket.
//
// Registering it adds "shiprocket" as a fulfillment provider, so an operator
// ships by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "shiprocket"` — the engine still owns the order's state and its
// events, and this module only talks to the carrier.
//
// Each store connects its own Shiprocket account under Settings › Shipping
// providers; Config is the install's fallback for any field a store leaves
// empty.
//
//	app, err := gocommerce.New(cfg,
//	    shiprocket.New(shiprocket.Config{
//	        Email:    os.Getenv("SHIPROCKET_EMAIL"),
//	        Password: os.Getenv("SHIPROCKET_PASSWORD"),
//	        PickupLocation: "Primary",
//	    }),
//	)
package shiprocket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

const (
	defaultBaseURL = "https://apiv2.shiprocket.in"
	// Shiprocket tokens last ten days; refreshing a day early avoids the
	// awkward case of a token expiring between two calls of one shipment.
	tokenLifetime = 9 * 24 * time.Hour
)

// Config configures the module.
type Config struct {
	// Email and Password are the Shiprocket API user's credentials. Required.
	Email    string `plugin:"email"`
	Password string `plugin:"password"`
	// PickupLocation is the nickname of the pickup address registered in
	// Shiprocket. Required — the carrier has to collect the parcel somewhere.
	PickupLocation string `plugin:"pickup_location"`
	// DefaultWeightKg is the last resort, per unit, for a parcel the engine
	// could not weigh — one holding a variant with no weight recorded. A
	// catalogue that is fully weighed never reaches it.
	DefaultWeightKg float64 `plugin:"default_weight_kg"`
	// DefaultLengthCm and friends are the last resort for a parcel the engine
	// has no unambiguous size for, which is any parcel holding more than a
	// single unit. See gocommerce.Parcel.
	DefaultLengthCm  float64
	DefaultBreadthCm float64
	DefaultHeightCm  float64
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Shiprocket fulfillment provider.
type Module struct {
	cfg    Config
	live   sync.Map // store id → *Config: each store has its own account
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
	cache  cache

	mu       sync.Mutex
	sessions map[string]session // store id → its account's token
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, sessions: map[string]session{}} }

// PluginKey is the plugin this module registers, where the panel keeps its
// credentials. Config is the environment's fallback for each field; a value
// typed into the panel wins.
const PluginKey = "shipping-shiprocket"

var errNotConfigured = errors.New("shiprocket: not set up — activate and configure it under Settings")

// conf is the effective configuration: the last one refresh computed, or
// Config alone before the first use.
func (m *Module) conf(ctx context.Context) *Config {
	store, _ := gocommerce.StoreFrom(ctx)
	if c, ok := m.live.Load(store); ok {
		return c.(*Config)
	}
	return &m.cfg
}

// refresh recomputes the effective configuration — Config with the plugin's
// settings laid over it — and reports whether the provider can work: switched
// on, with every required field filled. Called before each use, so a key
// typed into the panel a moment ago counts without a restart.
func (m *Module) refresh(ctx context.Context) bool {
	c := m.cfg
	on := false
	if m.app != nil {
		on, _ = m.app.Plugins().Enabled(ctx, PluginKey)
		if on {
			_ = m.app.Plugins().Fill(ctx, PluginKey, &c)
		}
	}
	m.finish(&c)
	store, _ := gocommerce.StoreFrom(ctx)
	m.live.Store(store, &c)
	return on && m.complete(&c)
}

// Configured implements gocommerce.Configurable.
func (m *Module) Configured(ctx context.Context) bool { return m.refresh(ctx) }

// complete is whether a configuration has everything the provider needs.
func (m *Module) complete(c *Config) bool {
	return strings.TrimSpace(c.Email) != "" &&
		strings.TrimSpace(c.Password) != "" &&
		strings.TrimSpace(c.PickupLocation) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.DefaultWeightKg <= 0 {
		c.DefaultWeightKg = 0.5
	}
	if c.DefaultLengthCm <= 0 {
		c.DefaultLengthCm, c.DefaultBreadthCm, c.DefaultHeightCm = 15, 15, 10
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-shiprocket" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration { return migrations }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Shiprocket", Icon: "ri-truck-line", Category: "shipping", DefaultEnabled: inCode,
		Home:        "/dash/shipping-settings/providers",
		Description: "Books shipments and waybills through Shiprocket, India's aggregator: one login across its couriers, with their rates, labels and pickups.",
		Docs:        "https://apidocs.shiprocket.in/",
		Fields: []gocommerce.PluginField{
			{Key: "email", Label: "API user email", Kind: "text", Required: !inCode, Help: "The API user's login (Settings › API in Shiprocket), not the account owner's."},
			{Key: "password", Label: "API user password", Kind: "secret", Required: !inCode},
			{Key: "pickup_location", Label: "Pickup location", Kind: "text", Required: !inCode, Help: "The pickup address nickname registered in Shiprocket. Test the connection to choose from the account's own."},
			{Key: "default_weight_kg", Label: "Default weight (kg)", Kind: "number", Help: "Per unit, for a variant with no weight recorded.", Default: 0.5},
			{Key: "base_url", Label: "API base URL", Kind: "url", Outbound: true, Help: "Empty for production."},
		},
	})
	m.finish(&m.cfg)
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = app.OutboundClient(30 * time.Second)
	}
	m.log = app.Log()

	app.RegisterFulfillment(m)
	m.routes(app)
	return nil
}

// Code implements gocommerce.FulfillmentProvider.
func (m *Module) Code() string { return "shiprocket" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Shiprocket" }

// Ship books the parcel: the Shiprocket order, its waybill, the pickup and the
// label, each step once.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table. Each step's answer is written
// to this module's own row as it arrives, so pressing Ship again after any
// failure resumes where it stopped instead of booking a second parcel.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	if err := domestic(order); err != nil {
		return gocommerce.Shipment{}, err
	}
	if paymentMethodFor(order) == "COD" && !wholeOrder(order, req.Lines) {
		return gocommerce.Shipment{}, gocommerce.Validationf(
			"a cash-on-delivery order goes out through Shiprocket in one parcel: the courier collects the order's total, " +
				"and how to split what it collects between parcels is not something to guess. Ship every item together, or ship by hand")
	}

	row, err := m.claim(ctx, order, contentsOf(req.Lines))
	if err != nil {
		return gocommerce.Shipment{}, err
	}
	defer m.release(ctx, row.ID)

	if row.ShipmentID == 0 {
		if err := m.createOrder(ctx, row, order, req); err != nil {
			return gocommerce.Shipment{}, err
		}
	}
	if row.AWB == "" {
		if err := m.assignAWB(ctx, row, req.Meta["courier_id"]); err != nil {
			// The order exists in Shiprocket but has no waybill. Say so
			// precisely: the operator needs to know the parcel is half-booked
			// rather than not booked at all — and that pressing Ship again
			// carries on from here.
			return gocommerce.Shipment{}, halfBooked(order, row, err)
		}
	}
	// Neither of these stops the parcel: it is booked, and both can be asked
	// for again from the order's Shiprocket card.
	if !row.PickupRequested {
		if err := m.requestPickup(ctx, row); err != nil {
			m.log.Warn("could not request a Shiprocket pickup", "shipment_id", row.ShipmentID, "error", err)
		}
	}
	if row.LabelURL == "" {
		if err := m.document(ctx, row, "label"); err != nil {
			m.log.Warn("could not generate a Shiprocket label", "shipment_id", row.ShipmentID, "error", err)
		}
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: row.AWB,
		Carrier:  carrierCode(row.CourierName),
		LabelURL: row.LabelURL,
	}, nil
}

func halfBooked(order *gocommerce.Order, row *shipment, err error) error {
	msg := fmt.Sprintf("Shiprocket order %s was created but no waybill was assigned: %s. Ship again to retry with the same order",
		row.ChannelOrderID, strings.TrimPrefix(errText(err), "Shiprocket said: "))
	var apiErr *gocommerce.APIError
	if errors.As(err, &apiErr) && apiErr.Status < 500 {
		return &gocommerce.APIError{Status: apiErr.Status, Code: apiErr.Code, Message: msg}
	}
	return fmt.Errorf("%s: %w", msg, err)
}

func errText(err error) string {
	var apiErr *gocommerce.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	return err.Error()
}

// domestic refuses what Shiprocket's domestic service cannot carry. Shiprocket
// X, its international service, is a different set of endpoints
// (/v1/external/international/…) and not part of this module yet.
func domestic(order *gocommerce.Order) error {
	if c := strings.ToUpper(strings.TrimSpace(order.Address.Country)); c != "IN" && c != "INDIA" {
		return gocommerce.Validationf("Shiprocket's domestic service delivers within India; this order is going to %s", order.Address.Country)
	}
	if order.Currency != "INR" {
		return gocommerce.Validationf("Shiprocket takes amounts in rupees, and this order is in %s", order.Currency)
	}
	return nil
}

// wholeOrder is whether a parcel is the entire order, nothing shipped before.
func wholeOrder(order *gocommerce.Order, lines []gocommerce.ShipLine) bool {
	in := map[int64]int{}
	for _, l := range lines {
		in[l.OrderLineID] += l.Quantity
	}
	for _, l := range order.Lines {
		if l.ShippedQuantity != 0 || in[l.ID] != l.Quantity {
			return false
		}
	}
	return true
}

// createOrder creates the order in Shiprocket. "Create Or Update Order ›
// Create Custom Order": POST /v1/external/orders/create/adhoc → {order_id,
// shipment_id}. Shiprocket refuses an order_id it has seen, so a retry whose
// first answer was lost looks the order up rather than creating it again.
func (m *Module) createOrder(ctx context.Context, row *shipment, order *gocommerce.Order, req gocommerce.ShipRequest) error {
	if row.createSent {
		if found, err := m.findOrder(ctx, row.ChannelOrderID); err == nil && found != nil {
			return m.adopt(ctx, row, found)
		}
	}
	if err := m.save(ctx, row.ID, map[string]any{"create_sent": true}); err != nil {
		return err
	}
	row.createSent = true

	var created struct {
		OrderID    int64  `json:"order_id"`
		ShipmentID int64  `json:"shipment_id"`
		Status     string `json:"status"`
		Message    string `json:"message"`
	}
	if err := m.call(ctx, http.MethodPost, "/v1/external/orders/create/adhoc", nil,
		m.orderPayload(ctx, order, req, row.ChannelOrderID), &created); err != nil {
		if found, ferr := m.findOrder(ctx, row.ChannelOrderID); ferr == nil && found != nil {
			return m.adopt(ctx, row, found)
		}
		return err
	}
	if created.ShipmentID == 0 {
		return refused(firstNonEmpty(created.Message, created.Status, "no shipment was created"))
	}
	row.SROrderID, row.ShipmentID = created.OrderID, created.ShipmentID
	return m.save(ctx, row.ID, map[string]any{"sr_order_id": created.OrderID, "shipment_id": created.ShipmentID})
}

// srOrder is what a search finds of an order this module created earlier.
type srOrder struct {
	ID         int64
	ShipmentID int64
	AWB        string
	Courier    string
}

// findOrder looks an order up by the id this module gave it. "Orders › Get
// all Orders": GET /v1/external/orders?search=<channel order id> →
// data[] {id, channel_order_id, shipments[] {id, awb, courier}}.
func (m *Module) findOrder(ctx context.Context, channelOrderID string) (*srOrder, error) {
	var out struct {
		Data []struct {
			ID             int64  `json:"id"`
			ChannelOrderID string `json:"channel_order_id"`
			Shipments      []struct {
				ID      int64  `json:"id"`
				AWB     string `json:"awb"`
				Courier string `json:"courier"`
			} `json:"shipments"`
		} `json:"data"`
	}
	if err := m.call(ctx, http.MethodGet, "/v1/external/orders",
		map[string][]string{"search": {channelOrderID}}, nil, &out); err != nil {
		return nil, err
	}
	for _, o := range out.Data {
		// The search matches waybills too; only the exact id is ours.
		if o.ChannelOrderID != channelOrderID || len(o.Shipments) == 0 {
			continue
		}
		s := o.Shipments[0]
		return &srOrder{ID: o.ID, ShipmentID: s.ID, AWB: s.AWB, Courier: s.Courier}, nil
	}
	return nil, nil
}

func (m *Module) adopt(ctx context.Context, row *shipment, found *srOrder) error {
	row.SROrderID, row.ShipmentID = found.ID, found.ShipmentID
	set := map[string]any{"sr_order_id": found.ID, "shipment_id": found.ShipmentID}
	if found.AWB != "" && row.AWB == "" {
		row.AWB, row.CourierName = found.AWB, found.Courier
		set["awb"], set["courier_name"] = found.AWB, found.Courier
	}
	return m.save(ctx, row.ID, set)
}

// orderPayload is the Create Custom Order body for this parcel.
func (m *Module) orderPayload(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest, channelOrderID string) map[string]any {
	addr := order.Address
	name := strings.TrimSpace(order.Name)
	if name == "" {
		name = strings.TrimSpace(addr.Name)
	}
	if name == "" {
		name = "Customer"
	}
	first, last := splitName(name)

	// What is declared is what is in this box, not what is on the order. The
	// engine has already resolved req.Lines to explicit quantities — on a
	// whole-order shipment that is every line at its full count — so a parcel
	// holding one of three lines does not tell the carrier it holds three, and
	// the declared value is not the whole order's.
	byLine := make(map[int64]gocommerce.OrderLine, len(order.Lines))
	for _, line := range order.Lines {
		byLine[line.ID] = line
	}
	items := make([]map[string]any, 0, len(req.Lines))
	var units int
	var valueMinor int64
	for _, ship := range req.Lines {
		line, ok := byLine[ship.OrderLineID]
		if !ok {
			continue
		}
		items = append(items, map[string]any{
			"name":          line.Title,
			"sku":           line.SKU,
			"units":         ship.Quantity,
			"selling_price": rupees(line.UnitPrice.AmountMinor),
		})
		units += ship.Quantity
		valueMinor += line.UnitPrice.AmountMinor * int64(ship.Quantity)
	}

	// sub_total is what Shiprocket takes as the order's amount: "the total is
	// not calculated automatically through the API". On cash on delivery it is
	// what the courier collects, so it is the order's own total — shipping,
	// discounts and tax as the engine settled them — and nothing is passed
	// beside it that Shiprocket could add or take away. Prepaid, it is what the
	// box is worth.
	method := paymentMethodFor(order)
	subTotal := valueMinor
	if method == "COD" {
		subTotal = order.Total.AmountMinor
	}

	conf := m.conf(ctx)
	return map[string]any{
		"order_id":              channelOrderID,
		"order_date":            order.CreatedAt.UTC().Format("2006-01-02 15:04"),
		"pickup_location":       conf.PickupLocation,
		"billing_customer_name": first,
		"billing_last_name":     last,
		"billing_address":       addr.Line1,
		"billing_address_2":     addr.Line2,
		"billing_city":          addr.City,
		"billing_pincode":       addr.PostalCode,
		"billing_state":         stateName(addr.Country, addr.State),
		"billing_country":       "India",
		"billing_email":         order.Email,
		"billing_phone":         indianPhone(firstNonEmpty(addr.Phone, order.Phone)),
		"shipping_is_billing":   true,
		"order_items":           items,
		"payment_method":        method,
		"sub_total":             rupees(subTotal),
		// Three sources, in this order: what the operator typed, what the
		// engine measured from the variants, and the configured fallback. The
		// operator comes first because they are holding the box; the measured
		// figure comes before the default because a catalogue somebody took
		// the trouble to weigh should not be overruled by a guess — which is
		// exactly what this module used to do.
		"length":  m.dimension(req.Meta, "length_cm", centimetres(req.Parcel.Dimensions.Length, conf.DefaultLengthCm)),
		"breadth": m.dimension(req.Meta, "breadth_cm", centimetres(req.Parcel.Dimensions.Width, conf.DefaultBreadthCm)),
		"height":  m.dimension(req.Meta, "height_cm", centimetres(req.Parcel.Dimensions.Height, conf.DefaultHeightCm)),
		"weight":  m.dimension(req.Meta, "weight_kg", m.weight(ctx, req.Parcel, units)),
	}
}

// assignAWB asks for a waybill. "Couriers › Generate AWB for Shipment":
// POST /v1/external/courier/assign/awb {shipment_id, courier_id?} →
// response.data {awb_code, courier_company_id, courier_name}. Without a
// courier id Shiprocket picks the account's default.
func (m *Module) assignAWB(ctx context.Context, row *shipment, courierID string) error {
	if row.awbSent {
		// An earlier request may have been answered after we stopped
		// listening; a second one would be refused, or worse, not.
		if found, err := m.findOrder(ctx, row.ChannelOrderID); err == nil && found != nil && found.AWB != "" {
			return m.adopt(ctx, row, found)
		}
	}
	if err := m.save(ctx, row.ID, map[string]any{"awb_sent": true}); err != nil {
		return err
	}
	row.awbSent = true

	payload := map[string]any{"shipment_id": row.ShipmentID}
	if courierID != "" {
		// The operator chose a courier in the ship dialog, which sends it
		// through the ship request's meta — exactly what that pass-through
		// is for.
		id, err := strconv.Atoi(courierID)
		if err != nil {
			return gocommerce.Validationf("courier_id %q is not a Shiprocket courier id", courierID)
		}
		payload["courier_id"] = id
	}
	var assigned struct {
		Status   int    `json:"awb_assign_status"`
		Message  string `json:"message"`
		Response struct {
			Data struct {
				AWBCode   string `json:"awb_code"`
				CourierID int    `json:"courier_company_id"`
				Courier   string `json:"courier_name"`
				Pickup    string `json:"pickup_scheduled_date"`
			} `json:"data"`
		} `json:"response"`
	}
	if err := m.call(ctx, http.MethodPost, "/v1/external/courier/assign/awb", nil, payload, &assigned); err != nil {
		return err
	}
	d := assigned.Response.Data
	if d.AWBCode == "" {
		return refused(firstNonEmpty(assigned.Message, "no waybill was assigned"))
	}
	row.AWB, row.CourierID, row.CourierName = d.AWBCode, d.CourierID, d.Courier
	return m.save(ctx, row.ID, map[string]any{"awb": d.AWBCode, "courier_id": d.CourierID, "courier_name": d.Courier})
}

// requestPickup asks the courier to collect. "Couriers › Request for Shipment
// Pickup": POST /v1/external/courier/generate/pickup {shipment_id: [id]} →
// {pickup_status, response {pickup_scheduled_date}}. It needs the waybill.
func (m *Module) requestPickup(ctx context.Context, row *shipment) error {
	var out struct {
		Status   int    `json:"pickup_status"`
		Message  string `json:"message"`
		Response struct {
			Scheduled string `json:"pickup_scheduled_date"`
			Data      string `json:"data"`
		} `json:"response"`
	}
	if err := m.call(ctx, http.MethodPost, "/v1/external/courier/generate/pickup", nil,
		map[string]any{"shipment_id": []int64{row.ShipmentID}}, &out); err != nil {
		return err
	}
	if out.Status != 1 {
		return refused(firstNonEmpty(out.Message, out.Response.Data, "the pickup was not scheduled"))
	}
	row.PickupRequested, row.PickupScheduled = true, out.Response.Scheduled
	return m.save(ctx, row.ID, map[string]any{"pickup_requested": true, "pickup_scheduled": out.Response.Scheduled})
}

// document fetches one of a parcel's papers and keeps its address.
//
//   - label: "Labels | Manifests | Invoice › Generate Label", POST
//     /v1/external/courier/generate/label {shipment_id: [id]} → label_url.
//   - manifest: "› Generate Manifest", POST /v1/external/manifests/generate
//     {shipment_id: [id]} → manifest_url; once generated, Shiprocket refuses
//     a second time, and "› Print Manifest", POST /v1/external/manifests/print
//     {order_ids: [id]} → manifest_url, prints the one it has.
//   - invoice: "› Generate Invoice", POST /v1/external/orders/print/invoice
//     {ids: [order id]} → invoice_url.
func (m *Module) document(ctx context.Context, row *shipment, kind string) error {
	var out struct {
		LabelURL    string `json:"label_url"`
		ManifestURL string `json:"manifest_url"`
		InvoiceURL  string `json:"invoice_url"`
		Response    string `json:"response"`
		Message     string `json:"message"`
	}
	var got, column string
	switch kind {
	case "label":
		if err := m.call(ctx, http.MethodPost, "/v1/external/courier/generate/label", nil,
			map[string]any{"shipment_id": []int64{row.ShipmentID}}, &out); err != nil {
			return err
		}
		got, column = out.LabelURL, "label_url"
	case "manifest":
		err := m.call(ctx, http.MethodPost, "/v1/external/manifests/generate", nil,
			map[string]any{"shipment_id": []int64{row.ShipmentID}}, &out)
		if err != nil || out.ManifestURL == "" {
			if perr := m.call(ctx, http.MethodPost, "/v1/external/manifests/print", nil,
				map[string]any{"order_ids": []int64{row.SROrderID}}, &out); perr != nil && err == nil {
				err = perr
			}
			if out.ManifestURL == "" && err != nil {
				return err
			}
		}
		got, column = out.ManifestURL, "manifest_url"
	case "invoice":
		if err := m.call(ctx, http.MethodPost, "/v1/external/orders/print/invoice", nil,
			map[string]any{"ids": []int64{row.SROrderID}}, &out); err != nil {
			return err
		}
		got, column = out.InvoiceURL, "invoice_url"
	default:
		return gocommerce.Validationf("no Shiprocket document called %q: label, manifest or invoice", kind)
	}
	if got == "" {
		return refused(firstNonEmpty(out.Message, out.Response, "no "+kind+" was generated"))
	}
	switch kind {
	case "label":
		row.LabelURL = got
	case "manifest":
		row.ManifestURL = got
	case "invoice":
		row.InvoiceURL = got
	}
	return m.save(ctx, row.ID, map[string]any{column: got})
}

// cancel calls off a booking at Shiprocket. With a waybill, "Shipments ›
// Cancel a Shipment": POST /v1/external/orders/cancel/shipment/awbs
// {awbs: [awb]}, which Shiprocket allows until the parcel is out for pickup;
// then the order itself, "Create Or Update Order › Cancel an Order": POST
// /v1/external/orders/cancel {ids: [order id]}.
func (m *Module) cancel(ctx context.Context, row *shipment) error {
	if row.AWB != "" {
		if err := m.call(ctx, http.MethodPost, "/v1/external/orders/cancel/shipment/awbs", nil,
			map[string]any{"awbs": []string{row.AWB}}, nil); err != nil {
			return err
		}
	}
	if row.SROrderID != 0 {
		if err := m.call(ctx, http.MethodPost, "/v1/external/orders/cancel", nil,
			map[string]any{"ids": []int64{row.SROrderID}}, nil); err != nil {
			return err
		}
	}
	now := time.Now()
	row.CancelledAt = &now
	return m.save(ctx, row.ID, map[string]any{"cancelled_at": now})
}

// paymentMethodFor tells the carrier whether to collect money on delivery.
// Getting this wrong means either a courier who does not ask for payment, or
// one who asks a customer who has already paid.
func paymentMethodFor(order *gocommerce.Order) string {
	if order.PaymentStatus == gocommerce.PaymentPaid {
		return "Prepaid"
	}
	return "COD"
}

// weight is what to declare, in kilograms.
//
// The engine's figure is used only when it measured every line: a Parcel whose
// Measured is false has a weight that is a floor, not the parcel's, and
// declaring a floor to a carrier is how a shipment comes back with a
// reweighing charge. In that case the configured default per unit is the more
// honest guess, because it is at least a guess about a whole parcel.
func (m *Module) weight(ctx context.Context, parcel gocommerce.Parcel, units int) float64 {
	if parcel.Measured && parcel.WeightGrams > 0 {
		return float64(parcel.WeightGrams) / 1000
	}
	// Rounded to the gram, which is all the precision a weight ever has here.
	// Without it, 0.4 kg three times is 1.2000000000000002 on the wire — not
	// wrong, but not something to put in front of a carrier either.
	return math.Round(m.conf(ctx).DefaultWeightKg*float64(max(units, 1))*1000) / 1000
}

// centimetres converts one of the engine's millimetre sides, falling back when
// the engine had no unambiguous answer — which it does not for any parcel
// holding more than a single unit. See gocommerce.Parcel for why.
func centimetres(mm *int, fallback float64) float64 {
	if mm == nil || *mm <= 0 {
		return fallback
	}
	return float64(*mm) / 10
}

func (m *Module) dimension(meta map[string]string, key string, fallback float64) float64 {
	if v, ok := meta[key]; ok {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

// carrierCode names the engine's carrier for a Shiprocket courier, so the
// order's tracking link goes to the courier that actually has the parcel.
// Shiprocket names a service ("Delhivery Surface", "Xpressbees 1kg"), and the
// company is its first word or two. A courier not listed here is left for
// the engine to read off the waybill.
func carrierCode(courier string) string {
	name := strings.ToLower(courier)
	for _, c := range []struct{ prefix, code string }{
		{"delhivery", "delhivery"}, {"blue dart", "bluedart"}, {"bluedart", "bluedart"},
		{"xpressbees", "xpressbees"}, {"ekart", "ekart"}, {"dtdc", "dtdc"},
		{"shadowfax", "shadowfax"}, {"ecom", "ecom-express"}, {"amazon", "amazon-shipping"},
		{"india post", "india-post"}, {"fedex", "fedex"}, {"aramex", "aramex"},
		{"gati", "gati"}, {"professional", "professional-couriers"}, {"trackon", "trackon"},
		{"dhl", "dhl"},
	} {
		if strings.HasPrefix(name, c.prefix) {
			return c.code
		}
	}
	return ""
}

// stateName is what Shiprocket expects a state to be called: the name, where
// the shopper's form sent a code.
func stateName(country, state string) string {
	if len(strings.TrimSpace(state)) > 3 {
		return state
	}
	for _, s := range gocommerce.StateSpellings(country, state) {
		if len(s) > 3 {
			return s
		}
	}
	return state
}

// indianPhone is the ten digits Shiprocket takes for an Indian number, from
// however the shopper typed it: "+91 98765 43210", "098765 43210".
func indianPhone(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		return d[2:]
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		return d[1:]
	}
	return d
}

func splitName(full string) (first, last string) {
	parts := strings.Fields(full)
	switch len(parts) {
	case 0:
		return "Customer", ""
	case 1:
		return parts[0], ""
	default:
		return parts[0], strings.Join(parts[1:], " ")
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
