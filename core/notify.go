package gocommerce

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
)

// notifierSet holds the delivery backends, per channel.
type notifierSet struct {
	mu        sync.RWMutex
	byChannel map[string][]notifierEntry
	log       *slog.Logger
	// record is the notification log's hook, called after every delivery
	// with what the backends said. Nil in a set built without an App.
	record func(ctx context.Context, note Notification, outcome notificationOutcome, err error)
}

// notifierEntry is a backend and the module that installed it. The module is
// carried because the question an operator asks about a channel is not "how
// many backends" but "who is sending my order confirmations", and a Notifier
// has no code of its own to answer with.
type notifierEntry struct {
	notifier Notifier
	module   string
}

func (n *notifierSet) add(channel string, no Notifier, module string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.byChannel == nil {
		n.byChannel = map[string][]notifierEntry{}
	}
	n.byChannel[channel] = append(n.byChannel[channel], notifierEntry{notifier: no, module: module})
}

func (n *notifierSet) forChannel(channel string) []notifierEntry {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.byChannel[channel]
}

// delivers reports whether a channel has a backend that will actually send
// something.
//
// Counting answers the wrong question: the built-in logger is registered for
// every channel at boot and add only appends, so len(targets) > 0 is always
// true — and RegisterNotifier's claim that "the built-in logger stands down"
// is aspirational rather than what the code does. Somebody locked out of the
// panel needs to know whether an email is really coming, not whether a line
// will be written to a log they cannot read.
func (n *notifierSet) delivers(channel string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, e := range n.byChannel[channel] {
		if _, isLog := e.notifier.(logNotifier); !isLog {
			return true
		}
	}
	return false
}

// describe reports one channel and what stands behind it, for the settings
// response and the doctor. Both ask the same question, so neither gets to
// answer it differently.
func (n *notifierSet) describe(channel string) NotifierChannelInfo {
	// Snapshot under the lock and ask the backends outside it: DisplayName is a
	// module's code, and the read lock is not something to hand to a third
	// party — a notifier that registered another one from it would deadlock the
	// settings route rather than fail.
	entries := n.forChannel(channel)

	info := NotifierChannelInfo{Channel: channel, Backends: make([]NotifierBackend, 0, len(entries))}
	for _, e := range entries {
		b := NotifierBackend{Name: e.module, Module: e.module}
		if _, isLog := e.notifier.(logNotifier); isLog {
			// Named on the value rather than on the type: "core" is true of
			// the built-in logger's module and says nothing about what it
			// does, and what it does is the whole point of this row.
			b.Name = "log"
		} else {
			b.Delivers = true
			info.Delivers = true
			if named, ok := e.notifier.(Named); ok && named.DisplayName() != "" {
				b.Name = named.DisplayName()
			}
		}
		info.Backends = append(info.Backends, b)
	}
	return info
}

// send delivers on one channel. Every notifier gets the message even if an
// earlier one failed, so a broken vendor does not silence the others; the
// aggregated error asks the outbox to retry the whole event.
func (n *notifierSet) send(ctx context.Context, note Notification) error {
	return n.sendFrom(ctx, note, nil)
}

// sendFrom is send with the row a resend points back at.
func (n *notifierSet) sendFrom(ctx context.Context, note Notification, resendOf *int64) error {
	targets := n.forChannel(note.Channel)
	outcome := notificationOutcome{resendOf: resendOf}
	var failures []error
	for _, target := range targets {
		err := target.notifier.Notify(ctx, note)
		if _, isLog := target.notifier.(logNotifier); isLog {
			// The log is where a message goes when nothing else will carry
			// it; it is not a backend the row should claim delivered.
			continue
		}
		outcome.backends = append(outcome.backends, target.module)
		if err != nil {
			failures = append(failures, err)
		} else {
			outcome.delivered = true
		}
	}
	err := errors.Join(failures...)
	if n.record != nil {
		n.record(ctx, note, outcome, err)
	}
	return err
}

// logNotifier is the built-in backend: it writes the message to the log and
// nothing else. The engine defines the notification abstraction and emits the
// triggers; actually delivering email or SMS is a vendor's job, and a vendor
// is a module.
type logNotifier struct{ log *slog.Logger }

func (l logNotifier) Notify(ctx context.Context, n Notification) error {
	l.log.Info("notification (no delivery backend installed)",
		"event", n.Event, "channel", n.Channel, "to", n.To,
		"language", n.Language, "order", n.Data["order_number"])
	return nil
}

// subscribeNotifications wires order events to notification delivery.
//
// Notifications are downstream consumers of committed events, never part of
// the checkout transaction: a vendor outage must not be able to fail a sale.
func (a *App) subscribeNotifications() {
	// Deliberately order.* only. Subscribing the cart family would mean that on
	// the first sweep after M28 every stale basket in the table with an email on
	// it sends mail — and when and how often to chase an abandoned cart is a
	// marketing decision with opt-out obligations attached, not an engine
	// default. A recovery module subscribes to cart.abandoned and owns the
	// schedule.
	a.bus.subscribe("order.*", coreMigrationOwner, func(ctx context.Context, e Event) error {
		var ev OrderEvent
		if err := e.Decode(&ev); err != nil {
			return fmt.Errorf("decode %s payload: %w", e.Name, err)
		}
		return a.notifyOrder(ctx, e.Name, &ev)
	})
}

func (a *App) notifyOrder(ctx context.Context, event string, ev *OrderEvent) error {
	data := orderNotificationData(ev)
	lang := ev.Language
	if lang == "" {
		lang = a.cfg.DefaultLanguage
	}

	var failures []error
	if ev.Email != "" {
		if err := a.notifier.send(ctx, Notification{
			Event: event, Channel: ChannelEmail, To: ev.Email, Language: lang, Data: data,
		}); err != nil {
			failures = append(failures, fmt.Errorf("email: %w", err))
		}
	}
	if ev.Phone != "" {
		if err := a.notifier.send(ctx, Notification{
			Event: event, Channel: ChannelSMS, To: ev.Phone, Language: lang, Data: data,
		}); err != nil {
			failures = append(failures, fmt.Errorf("sms: %w", err))
		}
	}
	return errors.Join(failures...)
}

// orderNotificationData flattens an order event into template data. It is flat
// strings on purpose: a notifier module owns its templates, and the engine
// hands it values rather than opinions about wording.
func orderNotificationData(ev *OrderEvent) map[string]string {
	data := map[string]string{
		"order_id":       strconv.FormatInt(ev.OrderID, 10),
		"order_number":   ev.Number,
		"order_status":   ev.Status,
		"payment_status": ev.PaymentStatus,
		"payment_method": ev.Provider,
		"currency":       ev.Currency,
		"total_minor":    strconv.FormatInt(ev.TotalMinor, 10),
		"item_count":     strconv.Itoa(len(ev.Lines)),
		"customer_name":  ev.Name,
		"customer_email": ev.Email,
	}
	if ev.Tracking != "" {
		data["tracking"] = ev.Tracking
	}
	if ev.Reason != "" {
		data["reason"] = ev.Reason
	}
	// Only on order.refunded, exactly as tracking and reason are conditional.
	// Without them a notifier hearing the event can say no more than "a refund
	// happened", which is the wrong message for a partial one.
	if ev.Refund != nil {
		data["refund_amount_minor"] = strconv.FormatInt(ev.Refund.AmountMinor, 10)
		data["refunded_minor"] = strconv.FormatInt(ev.Refund.RefundedMinor, 10)
		data["refund_remaining_minor"] = strconv.FormatInt(ev.Refund.RemainingMinor, 10)
	}
	// Only on order.returned and order.unreturned. A notifier hearing the event
	// with none of these could say no more than "something came back", which is
	// the wrong message for one item out of three.
	if ev.Return != nil {
		data["return_id"] = strconv.FormatInt(ev.Return.ReturnID, 10)
		data["return_status"] = ev.Return.Status
		data["return_units"] = strconv.Itoa(ev.Return.Units)
		data["return_refundable_minor"] = strconv.FormatInt(ev.Return.RefundableMinor, 10)
	}
	// A one-line summary covers the common template without forcing every
	// notifier to walk the line array.
	summary := ""
	for i, l := range ev.Lines {
		if i > 0 {
			summary += ", "
		}
		summary += strconv.Itoa(l.Quantity) + " x " + l.Title
		if l.VariantLabel != "" {
			summary += " (" + l.VariantLabel + ")"
		}
	}
	data["items_summary"] = summary

	// What was in this parcel, beside what is in the whole order. item_count and
	// items_summary keep describing the order, which is an existing contract —
	// these are additional keys rather than a redefinition of those.
	if ev.Shipment != nil {
		parcel := ""
		for i, l := range ev.Shipment.Lines {
			if i > 0 {
				parcel += ", "
			}
			parcel += strconv.Itoa(l.Quantity) + " x " + l.Title
			if l.VariantLabel != "" {
				parcel += " (" + l.VariantLabel + ")"
			}
		}
		data["shipment_items_summary"] = parcel
		data["shipment_item_count"] = strconv.Itoa(len(ev.Shipment.Lines))
		data["shipment_remaining_units"] = strconv.Itoa(ev.Shipment.RemainingUnits)
		// Present only when units are actually still owed. This map is
		// map[string]string and text/template treats every non-empty string as
		// true, so a key holding "0" would tell a customer the rest follows
		// separately on the parcel that completed their order.
		if ev.Shipment.RemainingUnits > 0 {
			data["shipment_is_partial"] = "true"
		}
	}
	return data
}
