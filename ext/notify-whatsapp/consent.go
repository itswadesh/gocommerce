package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// callingCodes is the country code a number typed without one is read in, by
// the store's country (D156 made it public). A store elsewhere asks its
// shoppers to type the + and the code.
// ponytail: the countries GoCommerce stores sell from today; add a row when a
// store elsewhere opts its shoppers in.
var callingCodes = map[string]string{
	"IN": "91", "US": "1", "CA": "1", "GB": "44", "AU": "61", "NZ": "64", "AE": "971",
	"SA": "966", "QA": "974", "SG": "65", "MY": "60", "ID": "62", "BD": "880", "NP": "977",
	"LK": "94", "PK": "92", "ZA": "27", "NG": "234", "KE": "254", "IE": "353", "DE": "49",
	"FR": "33", "BR": "55", "MX": "52",
}

// e164 reads a number as WhatsApp addresses it: "+", the country code and the
// number, fifteen digits at most. A number written with + or 00 keeps its own
// code; one without is the store's country's, its trunk 0 dropped.
func e164(raw, country string) (string, bool) {
	raw = strings.TrimSpace(raw)
	intl := strings.HasPrefix(raw, "+")
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, raw)
	switch {
	case intl:
	case strings.HasPrefix(digits, "00"):
		digits = digits[2:]
	default:
		code := callingCodes[strings.ToUpper(country)]
		if code == "" {
			return "", false
		}
		digits = strings.TrimLeft(digits, "0")
		// "919876543210" typed by an Indian shopper already carries the code;
		// a ten-digit number does not.
		if !(strings.HasPrefix(digits, code) && len(digits) > len(code)+8) {
			digits = code + digits
		}
	}
	if len(digits) < 8 || len(digits) > 15 || digits[0] == '0' {
		return "", false
	}
	return "+" + digits, true
}

func (m *Module) normalize(ctx context.Context, raw string) (string, bool) {
	country := ""
	if p, err := m.app.Profile().Get(ctx); err == nil {
		country = p.Country
	}
	return e164(raw, country)
}

// consented is whether the number's owner said yes to this kind of message
// and has not said STOP since.
func (m *Module) consented(ctx context.Context, phone string, marketing bool) bool {
	var ok bool
	err := m.db.QueryRowContext(ctx, `
		SELECT opted_out_at IS NULL AND CASE WHEN $2 THEN marketing ELSE order_updates END
		FROM whatsapp_contacts WHERE phone = $1`, phone, marketing).Scan(&ok)
	return err == nil && ok
}

// ConsentInput is what the storefront's opt-in boxes post.
type ConsentInput struct {
	CartID       string `json:"cart_id"`
	Phone        string `json:"phone"`
	OrderUpdates bool   `json:"order_updates"`
	Marketing    bool   `json:"marketing"`
}

// Consent is a number's standing answer.
type Consent struct {
	Phone        string `json:"phone"`
	OrderUpdates bool   `json:"order_updates"`
	Marketing    bool   `json:"marketing"`
}

// RecordConsent stores the shopper's answer to the boxes, the latest answer
// for a number winning, and puts the number on the basket so a reminder about
// it can reach them. An explicit yes after a STOP is a fresh opt-in, which is
// what a shopper ticking the box again means.
func (m *Module) RecordConsent(ctx context.Context, in ConsentInput) (*Consent, error) {
	phone, ok := m.normalize(ctx, in.Phone)
	if !ok {
		return nil, gocommerce.Validationf("enter a WhatsApp number with its country code, such as +91 98765 43210")
	}
	cart, err := m.app.Cart().GetByToken(ctx, strings.TrimSpace(in.CartID))
	if err != nil {
		return nil, err
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO whatsapp_contacts (phone, order_updates, marketing, consent_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (store_id, phone) DO UPDATE
		SET order_updates = $2, marketing = $3, consent_at = now(),
		    opted_out_at = CASE WHEN $2 OR $3 THEN NULL ELSE whatsapp_contacts.opted_out_at END,
		    updated_at = now()`, phone, in.OrderUpdates, in.Marketing); err != nil {
		return nil, gocommerce.Internalf(err, "record whatsapp consent")
	}
	// Only a basket whose shopper agreed to reminders carries the number:
	// nothing else reads it, and it is a contact detail we were not asked to
	// keep otherwise.
	keep := ""
	if in.Marketing {
		keep = phone
	}
	if keep != cart.Phone {
		if _, err := m.app.Cart().SetPhone(ctx, cart.Token, keep); err != nil {
			return nil, err
		}
	}
	return &Consent{Phone: phone, OrderUpdates: in.OrderUpdates, Marketing: in.Marketing}, nil
}

// setOptOut records STOP (true) or START (false) from the number itself.
func (m *Module) setOptOut(ctx context.Context, phone string, out bool) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO whatsapp_contacts (phone, opted_out_at, last_inbound_at)
		VALUES ($1, CASE WHEN $2 THEN now() END, now())
		ON CONFLICT (store_id, phone) DO UPDATE
		SET opted_out_at = CASE WHEN $2 THEN now() END, last_inbound_at = now(), updated_at = now()`,
		phone, out)
	return err
}

// heard records that the number wrote to the store, which opens WhatsApp's
// 24-hour customer service window.
func (m *Module) heard(ctx context.Context, phone string) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO whatsapp_contacts (phone, last_inbound_at) VALUES ($1, now())
		ON CONFLICT (store_id, phone) DO UPDATE SET last_inbound_at = now(), updated_at = now()`, phone)
	return err
}

// serviceWindow is how long after the shopper's last message a reply that is
// not a template may go (Meta's customer service window).
const serviceWindow = 24 * time.Hour

func (m *Module) windowOpen(ctx context.Context, phone string) bool {
	var open bool
	err := m.db.QueryRowContext(ctx, `
		SELECT last_inbound_at > now() - make_interval(secs => $2)
		FROM whatsapp_contacts WHERE phone = $1`, phone, serviceWindow.Seconds()).Scan(&open)
	return err == nil && open
}

// ------------------------------------------------------------------ routes

// handleConsentState tells the storefront whether to show the boxes: the
// order updates one once an order template is mapped, the offers one once the
// basket reminder is.
func (m *Module) handleConsentState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := struct {
		Enabled   bool `json:"enabled"`
		Marketing bool `json:"marketing"`
	}{}
	if m.account(ctx).ready() {
		maps, err := m.mappings(ctx)
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read whatsapp templates"))
			return
		}
		for slot := range maps {
			if slot == "cart_reminder" {
				out.Marketing = true
			} else {
				out.Enabled = true
			}
		}
		out.Marketing = out.Marketing && out.Enabled
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleConsent(w http.ResponseWriter, r *http.Request) {
	var in ConsentInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if strings.TrimSpace(in.CartID) == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("cart_id is required"))
		return
	}
	c, err := m.RecordConsent(r.Context(), in)
	if err != nil {
		if errors.Is(err, gocommerce.ErrNotFound) {
			err = gocommerce.NotFoundf("cart not found")
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, c)
}
