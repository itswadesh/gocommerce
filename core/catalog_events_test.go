package gocommerce

import (
	"context"
	"encoding/json"
	"testing"
)

// The catalogue announcing itself.
//
// Until these existed, every event this engine emitted was an order or a cart,
// so anything watching the shop's own pages — a search-engine ping, a cache, a
// static site — had nothing to watch and had to poll. The tests below are about
// the two things a consumer cannot recover if the event is wrong: which address
// changed, and whether the page should still exist.

// outboxEvents reads what a transaction announced.
func outboxEvents(t *testing.T, app *App, name string) []map[string]any {
	t.Helper()
	rows, err := app.DB().QueryContext(context.Background(),
		`SELECT payload FROM outbox_events WHERE event_name = $1 ORDER BY id`, name)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out = append(out, payload)
	}
	return out
}

func TestTheCatalogueAnnouncesItself(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(2500)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Linen field jumper", Status: "active", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	created := outboxEvents(t, app, EventProductCreated)
	if len(created) != 1 {
		t.Fatalf("created events = %d, want 1", len(created))
	}
	if created[0]["slug"] != "linen-field-jumper" {
		t.Errorf("slug = %v", created[0]["slug"])
	}
	if created[0]["status"] != "active" {
		t.Errorf("status = %v", created[0]["status"])
	}

	// An edit that changes nothing a shopper sees still announces: a consumer
	// deciding whether to act on it is better placed than this is.
	title := "Linen field jumper II"
	if _, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{Title: &title}); err != nil {
		t.Fatalf("update: %v", err)
	}
	updated := outboxEvents(t, app, EventProductUpdated)
	if len(updated) != 1 {
		t.Fatalf("updated events = %d, want 1", len(updated))
	}
	if updated[0]["title"] != title {
		t.Errorf("title = %v, want the new one", updated[0]["title"])
	}
	// The handle did not move, so nothing is left behind.
	if got, ok := updated[0]["previous_slug"]; ok && got != "" {
		t.Errorf("previous_slug = %v on an edit that did not rename", got)
	}
}

// A renamed handle leaves an address behind, and after the commit nothing can
// say what it was. A consumer retiring the old URL has only this.
func TestARenamedHandleCarriesTheOldOne(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(2500)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Linen field jumper", Status: "active", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	slug := "linen-field-jumper-2026"
	if _, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{Slug: &slug}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	updated := outboxEvents(t, app, EventProductUpdated)
	if len(updated) != 1 {
		t.Fatalf("updated events = %d, want 1", len(updated))
	}
	if updated[0]["slug"] != slug {
		t.Errorf("slug = %v, want the new one", updated[0]["slug"])
	}
	if updated[0]["previous_slug"] != "linen-field-jumper" {
		t.Errorf("previous_slug = %v, want the address that just stopped working",
			updated[0]["previous_slug"])
	}
}

// Taking a product off sale is an update, not a delete. A consumer that treats
// them the same tells a search engine to drop a URL that should be recrawled.
func TestArchivingIsAnUpdateAndNotADelete(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(2500)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Linen field jumper", Status: "active", PriceMinor: &price,
	})
	if err != nil {
		t.Fatal(err)
	}

	archived := "archived"
	if _, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{Status: &archived}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got := outboxEvents(t, app, EventProductDeleted); len(got) != 0 {
		t.Errorf("archiving emitted %d delete events, want none", len(got))
	}
	updated := outboxEvents(t, app, EventProductUpdated)
	if len(updated) != 1 || updated[0]["status"] != "archived" {
		t.Errorf("updated = %+v, want one carrying archived", updated)
	}

	// And a real delete says so, with the address it took away.
	if err := app.Products().DeleteProduct(ctx, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deleted := outboxEvents(t, app, EventProductDeleted)
	if len(deleted) != 1 {
		t.Fatalf("delete events = %d, want 1", len(deleted))
	}
	if deleted[0]["slug"] != "linen-field-jumper" {
		t.Errorf("slug = %v, want the address that is now gone", deleted[0]["slug"])
	}
}

// The event and the row commit together, which is the whole point of an
// outbox: a refused write must not announce a change that did not happen.
func TestARefusedWriteAnnouncesNothing(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(2500)

	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Linen field jumper", Status: "active", PriceMinor: &price,
	}); err != nil {
		t.Fatal(err)
	}
	before := len(outboxEvents(t, app, EventProductCreated))

	// Same slug: refused by the unique index, inside the transaction.
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Linen field jumper", Status: "active", PriceMinor: &price,
	}); err == nil {
		t.Fatal("a duplicate slug was accepted")
	}
	if after := len(outboxEvents(t, app, EventProductCreated)); after != before {
		t.Errorf("a refused create announced itself: %d events, was %d", after, before)
	}
}
