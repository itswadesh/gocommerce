package wishlist

import (
	"context"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

type recorderModule struct{ rec *gctest.RecordingNotifier }

func (recorderModule) Name() string                       { return "recorder" }
func (recorderModule) Migrations() []gocommerce.Migration { return nil }
func (m recorderModule) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelEmail, m.rec)
	return nil
}

// A shopper who saved something sold out hears once when it is back, not on
// every pass while it stays in stock, and again only after it sells out and
// returns.
func TestARestockIsAnnouncedOnce(t *testing.T) {
	ctx := context.Background()
	rec := &gctest.RecordingNotifier{}
	mod := New(Config{})
	app := gctest.New(t, mod, recorderModule{rec})
	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"collect_email": true}}); err != nil {
		t.Fatal(err)
	}
	gone := gctest.CreateProduct(t, app, "WL-BACK", 1500, 0)
	inStock := gctest.CreateProduct(t, app, "WL-HERE", 900, 4)
	list := start(t, app, "asha@example.com")
	save(t, app, list.Token, ItemInput{ProductID: gone.ID})
	save(t, app, list.Token, ItemInput{ProductID: inStock.ID})
	nobody := start(t, app, "")
	save(t, app, nobody.Token, ItemInput{ProductID: gone.ID})

	pass := func(want int) {
		t.Helper()
		n, err := mod.RestockPass(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("sent = %d, want %d", n, want)
		}
	}
	pass(0) // sees it sold out; saved-while-in-stock is never news
	if _, err := app.Stock().Adjust(ctx, gone.DefaultVariant().ID, 0, 3, "received"); err != nil {
		t.Fatal(err)
	}
	pass(1)
	pass(0)

	all := rec.All()
	if len(all) != 1 || all[0].To != "asha@example.com" || all[0].Data["product_title"] != gone.Title {
		t.Fatalf("sent %+v, want one message to the list's address naming the product", all)
	}

	// Sold out and back again is a second restock.
	if _, err := app.Stock().Adjust(ctx, gone.DefaultVariant().ID, 0, -3, "count"); err != nil {
		t.Fatal(err)
	}
	pass(0)
	if _, err := app.Stock().Adjust(ctx, gone.DefaultVariant().ID, 0, 2, "received"); err != nil {
		t.Fatal(err)
	}
	pass(1)
}
