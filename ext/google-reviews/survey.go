package googlereviews

import (
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Survey is what Google Customer Reviews' opt-in module is rendered with on
// the order confirmation page, every field from the order itself
// (support.google.com/merchants/answer/7106244). All five but OptInStyle are
// required by Google; with any of them unknown there is no survey, never a
// guess.
type Survey struct {
	MerchantID int64  `json:"merchant_id"`
	OrderID    string `json:"order_id"`
	Email      string `json:"email"`
	// DeliveryCountry is ISO 3166-1 alpha-2, never "ZZ".
	DeliveryCountry string `json:"delivery_country"`
	// EstimatedDeliveryDate is YYYY-MM-DD: the day the order was placed plus
	// the store's delivery estimate. Google emails the survey after it.
	EstimatedDeliveryDate string `json:"estimated_delivery_date"`
	OptInStyle            string `json:"opt_in_style,omitempty"`
}

// handleSurvey answers the confirmation page of the shopper holding the
// order's access token, the same credential that page reads the order with.
func (m *Module) handleSurvey(w http.ResponseWriter, r *http.Request) {
	none := gocommerce.NotFoundf("no Google Customer Reviews survey for this order")
	s, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	merchant, err := strconv.ParseInt(s.merchantID, 10, 64)
	if !s.customerReviews || err != nil || merchant <= 0 || s.deliveryDays <= 0 {
		gocommerce.RespondError(w, r, none)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		gocommerce.RespondError(w, r, none)
		return
	}
	o, err := m.app.Order().GetForGuest(r.Context(), r.PathValue("number"), token)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// The store's own definition of a sale, the one a review is verified by:
	// an order awaiting payment, or cancelled, is not one to ask about.
	switch o.Status {
	case gocommerce.OrderConfirmed, gocommerce.OrderPartial, gocommerce.OrderShipped, gocommerce.OrderDelivered:
	default:
		gocommerce.RespondError(w, r, none)
		return
	}
	country := strings.ToUpper(strings.TrimSpace(o.Address.Country))
	addr, err := mail.ParseAddress(o.Email)
	if err != nil || len(country) != 2 || country == "ZZ" {
		gocommerce.RespondError(w, r, none)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	gocommerce.Respond(w, http.StatusOK, Survey{
		MerchantID: merchant, OrderID: o.Number, Email: addr.Address, DeliveryCountry: country,
		EstimatedDeliveryDate: o.CreatedAt.UTC().AddDate(0, 0, s.deliveryDays).Format("2006-01-02"),
		OptInStyle:            s.optInStyle,
	})
}
