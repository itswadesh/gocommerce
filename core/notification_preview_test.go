package gocommerce

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// A logged email previews as its wording filled with the data it carried, in
// the language it went out in, and says when the wording was edited since.
func TestALoggedEmailPreviewsWithItsOwnData(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "NOTE-PREVIEW")
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	rows, _, err := app.Notifications().List(ctx, NotificationQuery{Event: EventOrderCreated})
	if err != nil || len(rows) == 0 {
		t.Fatalf("no confirmation logged: %v", err)
	}
	id := rows[0].ID

	got, err := app.Notifications().Preview(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Subject, order.Number) || !strings.Contains(got.Body, order.Number) {
		t.Errorf("preview = %+v, want the order number filled in", got)
	}
	if got.Source != WordingDefault || got.EditedSince || got.Language != "en" {
		t.Errorf("preview source %q edited %v language %q", got.Source, got.EditedSince, got.Language)
	}

	// Reworded after it went out: the preview says the message may have read
	// differently.
	if _, err := app.NotifyTemplates().Set(ctx, ChannelEmail, EventOrderCreated, "Thanks for {{.order_number}}", "We have it."); err != nil {
		t.Fatal(err)
	}
	got, _ = app.Notifications().Preview(ctx, id)
	if got.Subject != "Thanks for "+order.Number || !got.EditedSince || got.Source != WordingOwn {
		t.Errorf("after rewording = %+v", got)
	}

	rec := do(t, app, http.MethodGet, "/api/admin/notifications/"+strconv.FormatInt(id, 10)+"/preview", withAdmin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"edited_since":true`) {
		t.Errorf("GET preview = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/notifications/999999/preview", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("a message that is not there = %d, want 404", rec.Code)
	}
}
