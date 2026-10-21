package msg91

import (
	"context"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// MSG91 is for India (the owner's rule): Indian numbers, and every number of
// a store whose country is India. Anything else is left to the store's other
// SMS provider — as is a message MSG91 has no DLT template for, since it
// would send nothing and the text would be lost.
func TestMSG91ServesIndianNumbersAndIndianStores(t *testing.T) {
	m := New(Config{AuthKey: "k", Templates: map[string]string{gocommerce.EventOrderShipped: "tpl-shipped"}})
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, m)
	ctx := context.Background()
	shipped := func(to string) gocommerce.Notification {
		return gocommerce.Notification{Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: to}
	}

	for to, want := range map[string]bool{
		"+91 98765 43210": true,  // Indian, with its code
		"919876543210":    true,  // the code without the plus
		"00919876543210":  true,  // dialled from abroad
		"+1 415 555 0100": false, // the United States
		"+447700900123":   false, // the United Kingdom
		"9876543210":      false, // no code, and a store not in India
	} {
		if got := m.Serves(ctx, shipped(to)); got != want {
			t.Errorf("%q: Serves = %v, want %v", to, got, want)
		}
	}
	if m.Serves(ctx, gocommerce.Notification{Event: gocommerce.EventOrderPaid, Channel: gocommerce.ChannelSMS, To: "+919876543210"}) {
		t.Error("served an event it has no template for")
	}

	// A store in India: every number is MSG91's, including one with no code.
	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Country = "IN"
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"9876543210", "+1 415 555 0100"} {
		if !m.Serves(ctx, shipped(to)) {
			t.Errorf("an India-based store's %q was not MSG91's", to)
		}
	}
}
