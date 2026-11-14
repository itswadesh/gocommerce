package wishlist

import (
	"context"
	"net/url"
	"strings"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// restockEvent names the message a shopper gets when something they saved is
// back, and the template the panel lets a store reword.
const restockEvent = "wishlist.back_in_stock"

// restockEvery is how often the pass looks. Stock that comes and goes inside
// five minutes was never really back for a shopper reading email.
const restockEvery = 5 * time.Minute

// itemAvailable is what the store could sell of item i this minute, the same
// sum the wishlist page shows, so "back in stock" means what the shopper saw
// change.
const itemAvailable = `coalesce((SELECT sum(CASE WHEN x.track_inventory THEN greatest(coalesce((SELECT sum(vs.on_hand - vs.reserved) FROM variant_stock vs WHERE vs.variant_id = x.id), 0), 0) ELSE 1000000 END)
                  FROM variants x
                 WHERE x.product_id = i.product_id AND x.active
                   AND (i.variant_id IS NULL OR x.id = i.variant_id)), 0)`

func (m *Module) registerRestock(app *gocommerce.App) {
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: restockEvent, Title: "Wishlist item back in stock",
		Description: "To a shopper whose wishlist holds something that was sold out and can be bought again.",
		Variables:   []string{"product_title", "product_link", "store_name"},
		Subject:     "{{.product_title}} is back in stock",
		Body: `{{.product_title}}, which you saved to your wishlist, is back in stock.
{{if .product_link}}
{{.product_link}}
{{end}}{{if .store_name}}
{{.store_name}}
{{end}}`,
	})
	app.OnStart(func(ctx context.Context) error {
		go func() {
			t := time.NewTicker(restockEvery)
			defer t.Stop()
			for {
				err := app.ForEachStore(ctx, func(ctx context.Context) error {
					_, err := m.RestockPass(ctx)
					return err
				})
				if err != nil && ctx.Err() == nil {
					app.Log().Error("wishlist: restock pass failed", "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
		return nil
	})
}

// RestockPass writes to every shopper whose saved item has come back since the
// last pass, for the store ctx is bound to, and returns how many were sent.
//
// There is no core event for stock becoming available — it changes through
// receipts, counts, moves, cancellations, returns and expiring reservations —
// so the pass compares instead (D248): an item seen at zero is marked waiting,
// and a waiting item seen above zero is cleared and announced. One message per
// restock, and none for something saved while it was already in stock.
func (m *Module) RestockPass(ctx context.Context) (int, error) {
	// collect_email is the store saying it means to write about restocks;
	// without it an address on a list was not given for this.
	if !m.enabled(ctx) || m.app.Plugins().String(ctx, pluginKey, "collect_email") != "true" {
		return 0, nil
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE wishlist_items i SET restock_waiting_since = now()
		WHERE i.restock_waiting_since IS NULL AND `+itemAvailable+` = 0`); err != nil {
		return 0, gocommerce.Internalf(err, "mark sold-out wishlist items")
	}
	// The claim commits before anything is sent (rule 5), and the WHERE on
	// restock_waiting_since is what keeps a second replica from claiming the
	// same row. ponytail: a crash between claim and send loses that one alert
	// rather than sending it twice; an outbox row is the upgrade if that matters.
	// An item on a list with no address is cleared all the same: it came back,
	// and an address added later is not a reason to announce an old restock.
	rows, err := m.db.QueryContext(ctx, `
		UPDATE wishlist_items i
		   SET restock_waiting_since = NULL,
		       restock_sent_at = CASE WHEN w.email <> '' THEN now() ELSE i.restock_sent_at END
		  FROM wishlists w, products p
		 WHERE w.id = i.wishlist_id AND p.id = i.product_id AND p.status = $1
		   AND i.restock_waiting_since IS NOT NULL AND `+itemAvailable+` > 0
		RETURNING w.email, p.title, p.slug`, gocommerce.ProductActive)
	if err != nil {
		return 0, gocommerce.Internalf(err, "claim restocked wishlist items")
	}
	type due struct{ email, title, slug string }
	var send []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.email, &d.title, &d.slug); err != nil {
			rows.Close()
			return 0, gocommerce.Internalf(err, "scan a restocked wishlist item")
		}
		if d.email != "" {
			send = append(send, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, gocommerce.Internalf(err, "claim restocked wishlist items")
	}
	if len(send) == 0 {
		return 0, nil
	}

	base, store := m.app.StorefrontURL(ctx), m.storeName(ctx)
	sent := 0
	for _, d := range send {
		link := ""
		if base != "" {
			link = base + strings.ReplaceAll(m.productPath(), "{slug}", url.PathEscape(d.slug))
		}
		// A failed send is on the notification log; the claim stands.
		if err := m.app.Notify(ctx, gocommerce.Notification{
			Event: restockEvent, Channel: gocommerce.ChannelEmail, To: d.email,
			Data: map[string]string{"product_title": d.title, "product_link": link, "store_name": store},
		}); err != nil {
			m.app.Log().Warn("wishlist: restock alert not sent", "error", err)
			continue
		}
		sent++
	}
	return sent, nil
}

func (m *Module) productPath() string {
	if m.cfg.ProductPath != "" {
		return m.cfg.ProductPath
	}
	return "/products/{slug}"
}

func (m *Module) storeName(ctx context.Context) string {
	if p, err := m.app.Profile().Get(ctx); err == nil && p.Name != "" {
		return p.Name
	}
	if id, ok := gocommerce.StoreFrom(ctx); ok {
		if st, err := m.app.Stores().Get(ctx, id); err == nil {
			return st.Name
		}
	}
	return ""
}
