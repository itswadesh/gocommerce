package telegramalerts

import (
	"context"
	"errors"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// onStoreCreated tells the install's owner that somebody opened a store.
//
// It sends from here rather than queueing as onEvent does: that queue belongs
// to a store, behind that store's switch and shown in its log, and this message
// is not the new store's business. The outbox is the queue instead, so an error
// here means Telegram could not be reached and the event comes back with
// backoff; a store's Events screen shows one that ran out of attempts.
//
// ponytail: holds the dispatcher for up to Timeout per store opened while
// Telegram is down, and a failing webhook consumer on the same event would send
// the message twice. A queue of its own if stores ever open by the thousand.
func (m *Module) onStoreCreated(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.StoreCreatedEvent
	if err := e.Decode(&ev); err != nil {
		m.log.Error("telegram alert: the new store's event cannot be read", "event_id", e.ID, "error", err)
		return nil
	}
	brand := m.app.StoreBrand(ctx)
	lines := []string{"New store on " + brand.Name + ": " + ev.Name}
	if brand.BaseDomain != "" {
		lines = append(lines, "https://"+ev.Slug+"."+brand.BaseDomain)
	}
	if ev.OwnerEmail != "" {
		lines = append(lines, "Owner: "+ev.OwnerEmail)
	}
	err := m.sendTo(ctx, bot{token: m.cfg.PlatformBotToken, chat: m.cfg.PlatformChatID}, strings.Join(lines, "\n"))
	var te *telegramError
	if errors.As(err, &te) && (te.refused() || te.badToken) {
		// Telegram will say the same on the twelfth try.
		m.log.Error("telegram alert: a new store could not be announced", "store", ev.StoreID, "reason", explain(te))
		return nil
	}
	return err
}
