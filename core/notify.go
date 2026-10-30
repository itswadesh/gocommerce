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

// ConfigurableNotifier is a backend whose credentials come from the Plugins
// screen rather than from Config. Until an operator has filled them in it is
// installed but cannot send, and the funnel, the settings and the doctor all
// treat it as absent — so "delivering" keeps meaning what it says. The same
// interface serves payment and fulfilment providers; see providers.go.
type ConfigurableNotifier = Configurable

// ready is whether a backend would actually carry a message right now.
func ready(ctx context.Context, n Notifier) bool {
	return configured(ctx, n)
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
func (n *notifierSet) delivers(ctx context.Context, channel string) bool {
	// Snapshot, then ask: Configured reads the plugins table, and a module's
	// code is not something to run under this lock.
	for _, e := range n.forChannel(channel) {
		if _, isLog := e.notifier.(logNotifier); isLog {
			continue
		}
		if ready(ctx, e.notifier) {
			return true
		}
	}
	return false
}

// carries is whether a backend that delivers would take note: delivers asked
// of the one message rather than the channel, since a scoped backend such as
// MSG91 texts only what it has a template for.
func (n *notifierSet) carries(ctx context.Context, note Notification) bool {
	for _, e := range carriers(ctx, n.forChannel(note.Channel), note) {
		if _, isLog := e.notifier.(logNotifier); isLog {
			continue
		}
		if ready(ctx, e.notifier) {
			return true
		}
	}
	return false
}

// describe reports one channel and what stands behind it, for the settings
// response and the doctor. Both ask the same question, so neither gets to
// answer it differently.
func (n *notifierSet) describe(ctx context.Context, channel string) NotifierChannelInfo {
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
			b.Delivers = ready(ctx, e.notifier)
			info.Delivers = info.Delivers || b.Delivers
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
	targets := carriers(ctx, n.forChannel(note.Channel), note)
	outcome := notificationOutcome{resendOf: resendOf}
	var failures []error
	for _, target := range targets {
		if _, isLog := target.notifier.(logNotifier); isLog {
			// The log is where a message goes when nothing else will carry
			// it; it is not a backend the row should claim delivered.
			_ = target.notifier.Notify(ctx, note)
			continue
		}
		if !ready(ctx, target.notifier) {
			// Installed, not yet configured: not a backend, so not a row
			// that says "sent" about a message nobody carried.
			continue
		}
		err := target.notifier.Notify(ctx, note)
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

// carriers is who takes this message on its channel (ScopedNotifier): the
// scoped backends that serve it, alone, when there are any that are ready;
// otherwise every backend that is not scoped. The log is always kept — it is
// where a message is written whoever carries it.
func carriers(ctx context.Context, all []notifierEntry, note Notification) []notifierEntry {
	var scoped, general []notifierEntry
	for _, e := range all {
		s, isScoped := e.notifier.(ScopedNotifier)
		switch {
		case !isScoped:
			general = append(general, e)
		case ready(ctx, e.notifier) && s.Serves(ctx, note):
			scoped = append(scoped, e)
		}
	}
	if len(scoped) == 0 {
		return general
	}
	out := scoped
	for _, e := range general {
		if _, isLog := e.notifier.(logNotifier); isLog {
			out = append(out, e)
		}
	}
	return out
}

// worthRetrying drops refusals from a send's error. The outbox retries what
// could not be delivered, not what a provider has already said no to; the
// refusal is on the notification's record either way. send joins one error per
// backend, so one level of unwrapping sees each.
func worthRetrying(err error) error {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		if errors.Is(err, ErrNotificationRefused) {
			return nil
		}
		return err
	}
	var keep []error
	for _, e := range joined.Unwrap() {
		if !errors.Is(e, ErrNotificationRefused) {
			keep = append(keep, e)
		}
	}
	return errors.Join(keep...)
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
	data := OrderNotificationData(ev)
	if link := a.orderTrackingURL(ctx, ev.OrderID, ev.Number); link != "" {
		data["order_tracking_url"] = link
	}
	// The language the customer ordered in, while the store still sells in
	// it, else the store's main language (D87).
	lang := a.profile.MessageLanguage(ctx, ev.Language)

	var failures []error
	if ev.Email != "" {
		if err := worthRetrying(a.notifier.send(ctx, Notification{
			Event: event, Channel: ChannelEmail, To: ev.Email, Language: lang, Data: data,
		})); err != nil {
			failures = append(failures, fmt.Errorf("email: %w", err))
		}
	}
	if ev.Phone != "" {
		if err := worthRetrying(a.notifier.send(ctx, Notification{
			Event: event, Channel: ChannelSMS, To: ev.Phone, Language: lang, Data: data,
		})); err != nil {
			failures = append(failures, fmt.Errorf("sms: %w", err))
		}
		// Only where a backend takes it: WhatsApp needs the shopper's consent
		// and an approved template for the event, and a store without either
		// should not log a row per order for a channel it does not use. The
		// module queues and returns, so a Meta outage is never this event's.
		wa := Notification{Event: event, Channel: ChannelWhatsApp, To: ev.Phone, Language: lang, Data: data}
		if a.notifier.carries(ctx, wa) {
			if err := worthRetrying(a.notifier.send(ctx, wa)); err != nil {
				failures = append(failures, fmt.Errorf("whatsapp: %w", err))
			}
		}
	}
	return errors.Join(failures...)
}

// OrderNotificationData flattens an order event into template data. It is flat
// strings on purpose: a notifier module owns its templates, and the engine
// hands it values rather than opinions about wording. Exported for a module
// that words its own messages about orders, so a key means the same thing there.
func OrderNotificationData(ev *OrderEvent) map[string]string {
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
		// What the carrier last said, on order.shipment_updated: the one fact
		// that tells "out for delivery" apart from any other tracking update.
		if ev.Shipment.TrackingStatus != "" {
			data["tracking_status"] = ev.Shipment.TrackingStatus
		}
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
