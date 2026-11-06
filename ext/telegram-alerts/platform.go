package telegramalerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	if owner := ev.OwnerEmail + ev.OwnerPhone; owner != "" {
		lines = append(lines, "Owner: "+owner)
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

const storeWebhookTimeout = 10 * time.Second

// onStoreCreatedWebhook posts the new store to the install owner's automation.
// An error sends the event back through the outbox, as onStoreCreated's does.
//
// The keys are the ones that automation already reads. user_first_name and
// user_last_name stay empty because the engine holds no person's name, and the
// business and address are whatever the new store has on file, usually nothing
// this early.
func (m *Module) onStoreCreatedWebhook(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.StoreCreatedEvent
	if err := e.Decode(&ev); err != nil {
		m.log.Error("store webhook: the new store's event cannot be read", "event_id", e.ID, "error", err)
		return nil
	}
	brand := m.app.StoreBrand(ctx)
	if want := strings.TrimSpace(m.cfg.PlatformWebhookBaseDomain); want != "" && !brandIsOn(brand, want) {
		return nil
	}
	profile, err := m.app.Profile().Get(ctx)
	if err != nil {
		return err
	}
	domain := ""
	if brand.BaseDomain != "" {
		domain = ev.Slug + "." + brand.BaseDomain
	}
	address := []string{}
	for _, part := range []string{profile.AddressLine1, profile.AddressLine2, profile.City, profile.State, profile.PostalCode} {
		if part != "" {
			address = append(address, part)
		}
	}
	body, err := json.Marshal(map[string]string{
		"store_name":      ev.Name,
		"business_name":   profile.LegalName,
		"domain":          domain,
		"user_first_name": "",
		"user_last_name":  "",
		"user_email":      ev.OwnerEmail,
		"user_phone":      ev.OwnerPhone,
		"address":         strings.Join(address, ", "),
		"country":         profile.Country,
	})
	if err != nil {
		return err
	}
	return m.postStoreWebhook(ctx, ev.StoreID, body)
}

// brandIsOn matches the brand's store domain, or failing that its panel's, since
// an install may give a brand an admin host and no store domain.
func brandIsOn(b gocommerce.Brand, domain string) bool {
	if b.BaseDomain == domain {
		return true
	}
	panel, err := url.Parse(b.PanelURL)
	return err == nil && strings.HasSuffix(panel.Hostname(), "."+domain)
}

func (m *Module) postStoreWebhook(ctx context.Context, storeID string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, storeWebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.PlatformWebhookURL, bytes.NewReader(body))
	if err != nil {
		// Not err: it quotes the address.
		m.log.Error("store webhook: the address is not a URL", "store", storeID)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	// The address is the owner's own, set in the environment, so an ordinary
	// client rather than app.OutboundClient (AGENTS.md rule 16).
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		// A client error quotes the whole address, and what is returned here is
		// shown on a store's Events screen.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("store webhook: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	switch {
	case res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("store webhook: answered %d", res.StatusCode)
	case res.StatusCode >= 300:
		// It will say the same on the twelfth try.
		m.log.Error("store webhook: a new store could not be announced", "store", storeID, "status", res.StatusCode)
	}
	return nil
}
