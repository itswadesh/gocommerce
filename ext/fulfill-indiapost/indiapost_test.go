package indiapost

import (
	"fmt"
	"strings"
	"testing"

	"github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

func newApp(t *testing.T, cfg Config) *gocommerce.App {
	t.Helper()
	return gctest.New(t, New(cfg))
}

func ship(t *testing.T, app *gocommerce.App, sku, tracking string) (*gocommerce.Order, error) {
	t.Helper()
	product := gctest.CreateProduct(t, app, sku, 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	return app.Ship().Create(t.Context(), result.Order.ID, "india-post",
		gocommerce.ShipRequest{Tracking: tracking})
}

// The module's reason to exist: the number is recorded against the right
// carrier, and it is recorded in the shape the engine will recognise.
func TestAConsignmentNumberIsRecorded(t *testing.T) {
	app := newApp(t, Config{})

	order, err := ship(t, app, "IP-1", "ee 123456789 in")
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "EE123456789IN" {
		t.Errorf("tracking = %q, want it normalised to EE123456789IN", shipment.Tracking)
	}
	if shipment.Carrier != "india-post" {
		t.Errorf("carrier = %q, want india-post", shipment.Carrier)
	}
	// The point of normalising: the stored number has to detect as India Post,
	// or the customer gets a number with no link.
	if detected, ok := gocommerce.DetectCarrier(shipment.Tracking); !ok || detected.Code != "india-post" {
		t.Errorf("the engine detected %+v for the stored number, want india-post", detected)
	}
}

// A transposed digit off a counter receipt surfaces a week later as a customer
// with a dead link. Catching it here is the whole difference between this
// provider and the manual one.
func TestANumberIndiaPostDidNotIssueIsRefused(t *testing.T) {
	app := newApp(t, Config{})

	for i, tracking := range []string{
		"1234567890123",          // a plain courier number
		"EE12345678IN",           // eight digits, not nine
		"EE123456789US",          // somebody else's country
		"9400111899223197428490", // USPS
	} {
		if _, err := ship(t, app, fmt.Sprintf("IP-BAD-%d", i), tracking); err == nil {
			t.Errorf("%q was accepted as an India Post consignment number", tracking)
		}
	}
}

// This provider records a booking; it cannot invent one.
func TestNoNumberIsRefused(t *testing.T) {
	app := newApp(t, Config{})

	_, err := ship(t, app, "IP-2", "")
	if err == nil {
		t.Fatal("a shipment with no consignment number was accepted")
	}
	if !strings.Contains(err.Error(), "consignment number") {
		t.Errorf("error = %v, want it to say what is missing", err)
	}
}

// India Post has products that number differently, and a store shipping one
// should be able to record what it was given.
func TestAcceptAnyNumberTakesTheCheckOff(t *testing.T) {
	app := newApp(t, Config{AcceptAnyNumber: true})

	order, err := ship(t, app, "IP-3", "123456789012")
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	if order.Fulfillments[0].Tracking != "123456789012" {
		t.Errorf("tracking = %q, want the number as given", order.Fulfillments[0].Tracking)
	}
	// The carrier is still recorded, so the number is never attributed to
	// whichever courier the engine's pattern matching would have guessed.
	if order.Fulfillments[0].Carrier != "india-post" {
		t.Errorf("carrier = %q, want india-post", order.Fulfillments[0].Carrier)
	}
}

// A refused number must leave the order unshipped, not half-shipped.
func TestARefusedNumberDoesNotShipTheOrder(t *testing.T) {
	app := newApp(t, Config{})
	product := gctest.CreateProduct(t, app, "IP-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	if _, err := app.Ship().Create(t.Context(), result.Order.ID, "india-post",
		gocommerce.ShipRequest{Tracking: "nope"}); err == nil {
		t.Fatal("a bad number was accepted")
	}
	order, err := app.Order().Get(t.Context(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if len(order.Fulfillments) != 0 || order.Status == gocommerce.OrderShipped {
		t.Errorf("order is %s with %d fulfillments, want unshipped", order.Status, len(order.Fulfillments))
	}
}
