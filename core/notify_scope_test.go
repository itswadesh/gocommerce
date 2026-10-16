package gocommerce

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// countingNotifier records who it was asked to carry.
type countingNotifier struct {
	mu sync.Mutex
	to []string
}

func (c *countingNotifier) Notify(_ context.Context, n Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.to = append(c.to, n.To)
	return nil
}

func (c *countingNotifier) sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.to...)
}

// indiaOnly is a provider for Indian numbers alone, the way MSG91 is.
type indiaOnly struct{ countingNotifier }

func (i *indiaOnly) Serves(_ context.Context, n Notification) bool {
	return strings.HasPrefix(n.To, "+91")
}

type smsPair struct {
	local  *indiaOnly
	global *countingNotifier
}

func (smsPair) Name() string            { return "sms-pair" }
func (smsPair) Migrations() []Migration { return nil }
func (p smsPair) Register(app *App) error {
	app.RegisterNotifier(ChannelSMS, p.global)
	app.RegisterNotifier(ChannelSMS, p.local)
	return nil
}

// A provider that carries only some recipients takes those alone: the store's
// other SMS provider stands aside for them, so nobody is texted twice, and
// carries everybody else.
func TestAScopedProviderTakesItsRecipientsAlone(t *testing.T) {
	pair := smsPair{local: &indiaOnly{}, global: &countingNotifier{}}
	app := newTestApp(t, pair)
	ctx := context.Background()
	for _, to := range []string{"+919876543210", "+14155550100"} {
		if err := app.Notify(ctx, Notification{Event: EventOrderShipped, Channel: ChannelSMS, To: to}); err != nil {
			t.Fatal(err)
		}
	}
	if got := pair.local.sent(); len(got) != 1 || got[0] != "+919876543210" {
		t.Fatalf("India-only provider carried %v, want only the Indian number", got)
	}
	if got := pair.global.sent(); len(got) != 1 || got[0] != "+14155550100" {
		t.Fatalf("the other provider carried %v, want only the US number", got)
	}
}

type onlyScoped struct{ local *indiaOnly }

func (onlyScoped) Name() string            { return "only-scoped" }
func (onlyScoped) Migrations() []Migration { return nil }
func (o onlyScoped) Register(app *App) error {
	app.RegisterNotifier(ChannelSMS, o.local)
	return nil
}

// A scoped provider does not carry what it does not serve, even when it is
// the only one.
func TestAScopedProviderLeavesWhatItDoesNotServe(t *testing.T) {
	o := onlyScoped{local: &indiaOnly{}}
	app := newTestApp(t, o)
	if err := app.Notify(context.Background(), Notification{Event: EventOrderShipped, Channel: ChannelSMS, To: "+14155550100"}); err != nil {
		t.Fatal(err)
	}
	if got := o.local.sent(); len(got) != 0 {
		t.Fatalf("an India-only provider texted %v", got)
	}
}
