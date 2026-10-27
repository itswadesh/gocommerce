package gocommerce

import (
	"context"
	"testing"
)

// An order's messages carry the storefront page where a guest opens the order
// with a code (D115): on the store's own domain once it has one, else its
// address under the base domain, and absent when the store has neither, since
// a link to nowhere is worse than none.
func TestOrderMessagesLinkToTheTrackingPage(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	ctx := WithStore(context.Background(), app.DefaultStore())
	st, err := app.Stores().Get(ctx, app.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	link := func() string {
		t.Helper()
		rec.mu.Lock()
		rec.sent = nil
		rec.mu.Unlock()
		if err := app.notifyOrder(ctx, EventOrderCreated, &OrderEvent{Number: "GC 64", Email: "asha@example.com"}); err != nil {
			t.Fatal(err)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if len(rec.sent) != 1 {
			t.Fatalf("sent %d messages, want 1", len(rec.sent))
		}
		return rec.sent[0].Data["order_tracking_url"]
	}

	app.cfg.BaseDomain = ""
	if got := link(); got != "" {
		t.Errorf("with no address the link = %q, want none", got)
	}

	app.cfg.BaseDomain = "shops.test"
	if got, want := link(), "https://"+st.Slug+".shops.test/order-tracking?number=GC+64"; got != want {
		t.Errorf("link = %q, want %q", got, want)
	}

	// An added but unproven domain is not yet the store's address.
	if _, err := app.db.ExecContext(ctx, `INSERT INTO store_domains (host, store_id) VALUES ('pending.example.com', $1)`, st.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(ctx, `UPDATE store_domains SET verified_at = NULL WHERE host = 'pending.example.com'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(ctx,
		`INSERT INTO store_domains (host, store_id, verified_at, is_primary) VALUES ('shop.example.com', $1, now(), true)`, st.ID); err != nil {
		t.Fatal(err)
	}
	if got, want := link(), "https://shop.example.com/order-tracking?number=GC+64"; got != want {
		t.Errorf("with a domain the link = %q, want %q", got, want)
	}
}
