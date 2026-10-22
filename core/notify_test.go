package gocommerce

import (
	"context"
	"testing"
)

// A module that words its own messages about orders — an alert to the owner
// rather than a note to the shopper — renders them over the same flat data the
// engine's templates read, so {{.order_number}} means one thing everywhere.
// Exported for that, and every key the engine's wording names has to be in it.
func TestOrderNotificationDataCarriesEveryKeyTheTemplatesName(t *testing.T) {
	data := OrderNotificationData(&OrderEvent{
		OrderID: 42, Number: "GC-000042", Status: "pending", PaymentStatus: "pending",
		Provider: "cod", Currency: "INR", TotalMinor: 129900,
		Email: "asha@example.com", Name: "Asha Rao",
		Lines:  []OrderEventLine{{Title: "Cotton kurta", VariantLabel: "M", Quantity: 2}},
		Reason: "changed my mind",
	})
	for _, key := range append([]string{"order_id", "reason"}, orderVariables...) {
		if _, ok := data[key]; !ok {
			t.Errorf("no %q in the flat data", key)
		}
	}
	if data["order_id"] != "42" || data["total_minor"] != "129900" || data["items_summary"] != "2 x Cotton kurta (M)" {
		t.Errorf("data = %v", data)
	}
}

// A module that hears its own event has, until now, no way to tell anybody
// about it. RegisterNotifier adds a delivery backend and the set that drives it
// is unexported, so `ext/cart-recovery` — which core/notify.go deliberately
// leaves to own the schedule for cart.abandoned — had nothing to deliver with
// short of shipping a transport of its own, which rule 2 forbids.
func TestNotifyReachesARegisteredNotifier(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})

	err := app.Notify(context.Background(), Notification{
		Event:   "cart.abandoned",
		Channel: ChannelEmail,
		To:      "shopper@example.com",
		Data:    map[string]string{"cart_token": "tok_1"},
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if got := rec.count("cart.abandoned"); got != 1 {
		t.Fatalf("cart.abandoned deliveries = %d, want 1", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if to := rec.sent[0].To; to != "shopper@example.com" {
		t.Errorf("To = %q, want shopper@example.com", to)
	}
	if tok := rec.sent[0].Data["cart_token"]; tok != "tok_1" {
		t.Errorf("Data[cart_token] = %q, want tok_1", tok)
	}
}

// The channel is the addressable thing here, and a typo in it would otherwise
// be silent: notifierSet.send over an unknown channel finds no backends and
// returns nil, so a module would report a delivery that never happened.
func TestNotifyRefusesAnUnknownChannel(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})

	err := app.Notify(context.Background(), Notification{
		Event:   "cart.abandoned",
		Channel: "pigeon",
		To:      "shopper@example.com",
	})
	if err == nil {
		t.Fatal("Notify over an unknown channel returned nil, want an error")
	}
	if rec.count("cart.abandoned") != 0 {
		t.Error("an unknown channel still delivered something")
	}
}

// A notification with nobody to send it to is a module bug, and one the module
// cannot see: an empty address reaches the vendor and fails there, or worse,
// does not.
func TestNotifyRefusesAnEmptyRecipient(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})

	err := app.Notify(context.Background(), Notification{
		Event:   "cart.abandoned",
		Channel: ChannelEmail,
		To:      "",
	})
	if err == nil {
		t.Fatal("Notify with no recipient returned nil, want an error")
	}
	if rec.count("cart.abandoned") != 0 {
		t.Error("an empty recipient still delivered something")
	}
}

// The store's default language is the one the templates fall back to, and a
// module has no business knowing what it is.
func TestNotifyFillsInTheDefaultLanguage(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})

	if err := app.Notify(context.Background(), Notification{
		Event:   "cart.abandoned",
		Channel: ChannelEmail,
		To:      "shopper@example.com",
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if lang := rec.sent[0].Language; lang != app.cfg.DefaultLanguage {
		t.Errorf("Language = %q, want the store default %q", lang, app.cfg.DefaultLanguage)
	}
}
