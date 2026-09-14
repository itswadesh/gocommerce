package usps

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// labelRequest is the part of the payload this package is responsible for
// getting right.
type labelRequest struct {
	ImageInfo          map[string]any `json:"imageInfo"`
	FromAddress        map[string]any `json:"fromAddress"`
	ToAddress          map[string]any `json:"toAddress"`
	PackageDescription struct {
		MailClass     string  `json:"mailClass"`
		RateIndicator string  `json:"rateIndicator"`
		WeightUOM     string  `json:"weightUOM"`
		Weight        float64 `json:"weight"`
		DimensionsUOM string  `json:"dimensionsUOM"`
		Length        float64 `json:"length"`
		Width         float64 `json:"width"`
		Height        float64 `json:"height"`
		MailingDate   string  `json:"mailingDate"`
	} `json:"packageDescription"`
}

const testPDF = "%PDF-1.4 pretend label"

type stub struct {
	mu          sync.Mutex
	last        labelRequest
	tokens      int
	payments    int
	paymentSeen string
	noImage     bool
	failWith    string
}

func (s *stub) get() labelRequest { s.mu.Lock(); defer s.mu.Unlock(); return s.last }

func newApp(t *testing.T, cfg Config, shape func(*stub)) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{}
	if shape != nil {
		shape(st)
	}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/v3/token":
			st.mu.Lock()
			st.tokens++
			st.mu.Unlock()
			_, _ = io.WriteString(w, `{"access_token":"bearer-1","token_type":"Bearer","expires_in":28800}`)

		case "/payments/v3/payment-authorization":
			if r.Header.Get("Authorization") != "Bearer bearer-1" {
				http.Error(w, `{"error":{"message":"no bearer"}}`, http.StatusUnauthorized)
				return
			}
			st.mu.Lock()
			st.payments++
			st.mu.Unlock()
			_, _ = io.WriteString(w, `{"paymentAuthorizationToken":"pay-1"}`)

		case "/labels/v3/label":
			// Both credentials, or USPS answers 401 with a perfectly valid
			// bearer — the failure this test exists to keep from coming back.
			if r.Header.Get("Authorization") != "Bearer bearer-1" ||
				r.Header.Get("X-Payment-Authorization-Token") != "pay-1" {
				http.Error(w, `{"error":{"message":"payment authorization is required"}}`, http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var in labelRequest
			_ = json.Unmarshal(body, &in)
			st.mu.Lock()
			st.last = in
			st.paymentSeen = r.Header.Get("X-Payment-Authorization-Token")
			failWith, noImage := st.failWith, st.noImage
			st.mu.Unlock()

			switch {
			case failWith != "":
				http.Error(w, `{"error":{"message":"`+failWith+`"}}`, http.StatusBadRequest)
			case noImage:
				_, _ = io.WriteString(w, `{"labelMetadata":{"trackingNumber":"9400111899223197428490"}}`)
			default:
				_, _ = io.WriteString(w, `{"labelMetadata":{"trackingNumber":"9400111899223197428490","postage":8.35},"labelImage":"`+
					base64.StdEncoding.EncodeToString([]byte(testPDF))+`"}`)
			}

		default:
			http.NotFound(w, r)
		}
	})

	cfg.ClientID, cfg.ClientSecret = "id", "secret"
	cfg.CRID, cfg.MID, cfg.AccountNumber = "1111", "2222", "3333"
	cfg.BaseURL = server.URL
	if cfg.MailClass == "" {
		cfg.MailClass = "USPS_GROUND_ADVANTAGE"
	}
	if cfg.From.StreetAddress == "" {
		cfg.From = Address{
			FirstName: "Acme", StreetAddress: "4120 Bingham Ave",
			City: "St. Louis", State: "MO", ZIPCode: "63116",
		}
	}
	return gctest.New(t, New(cfg)), st
}

func TestRegisterRequiresConfiguration(t *testing.T) {
	t.Parallel()

	full := Config{
		ClientID: "id", ClientSecret: "secret", CRID: "1", MID: "2",
		AccountNumber: "3", MailClass: "USPS_GROUND_ADVANTAGE",
		From: Address{StreetAddress: "1 A St", ZIPCode: "63116"},
	}
	withoutCRID := full
	withoutCRID.CRID = ""
	withoutFrom := full
	withoutFrom.From = Address{}
	withoutMailClass := full
	withoutMailClass.MailClass = ""

	for _, cfg := range []Config{{}, withoutCRID, withoutFrom, withoutMailClass} {
		if err := New(cfg).Register(nil); err == nil {
			t.Errorf("Register accepted an incomplete config: %+v", cfg)
		}
	}
}

// The module's reason to exist, including the part that is unique to USPS: the
// label is bytes, and there has to be somewhere to print it from.
func TestLabelIsBoughtStoredAndServed(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.MeasuredProduct(t, app, "US-1", 2500, 10, 750, 305, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "usps", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "9400111899223197428490" {
		t.Errorf("tracking = %q, want the number USPS issued", shipment.Tracking)
	}
	if shipment.Carrier != "usps" {
		t.Errorf("carrier = %q, want usps", shipment.Carrier)
	}
	want := "/api/admin/x/fulfill-usps/labels/9400111899223197428490"
	if shipment.LabelURL != want {
		t.Errorf("label = %q, want %q", shipment.LabelURL, want)
	}

	rec := gctest.AdminRequest(t, app, http.MethodGet, shipment.LabelURL, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET the label = %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != testPDF {
		t.Errorf("label body = %q, want the PDF USPS issued", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/pdf" {
		t.Errorf("content type = %q, want application/pdf", got)
	}

	sent := st.get()
	// A parcel declared lighter than it is comes back with postage due, so
	// 750 g is 1.66 lb rounded up, not 1.65.
	if sent.PackageDescription.Weight != 1.66 || sent.PackageDescription.WeightUOM != "lb" {
		t.Errorf("weight = %v %s, want 1.66 lb", sent.PackageDescription.Weight, sent.PackageDescription.WeightUOM)
	}
	if sent.PackageDescription.Length != 12.01 || sent.PackageDescription.DimensionsUOM != "in" {
		t.Errorf("length = %v %s, want 12.01 in from 305 mm",
			sent.PackageDescription.Length, sent.PackageDescription.DimensionsUOM)
	}
	if sent.ToAddress["ZIPCode"] != "94117" {
		t.Errorf("ZIPCode = %v, want the order's", sent.ToAddress["ZIPCode"])
	}
	if sent.FromAddress["streetAddress"] != "4120 Bingham Ave" {
		t.Errorf("fromAddress = %v, want the configured ship-from", sent.FromAddress)
	}
	if sent.PackageDescription.MailClass != "USPS_GROUND_ADVANTAGE" {
		t.Errorf("mailClass = %q, want the configured class", sent.PackageDescription.MailClass)
	}
}

// A label carries the buyer's name and address, so it is gated like the order.
func TestLabelNeedsOrdersRead(t *testing.T) {
	app, _ := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "US-2", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	order := gctest.Ship(t, app, result.Order.ID, "usps", gocommerce.ShipRequest{})
	labelURL := order.Fulfillments[0].LabelURL

	rec := gctest.Request(t, app, http.MethodGet, labelURL, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET = %d, want 401", rec.Code)
	}

	gctest.AssertAdminRoutesDeclareRights(t, app, "fulfill-usps")
	gctest.AssertSpecCoversModuleRoutes(t, app, "fulfill-usps")
}

// A tracking number with no stored label is a 404, not an empty PDF.
func TestUnknownLabelIsNotFound(t *testing.T) {
	app, _ := newApp(t, Config{}, nil)
	rec := gctest.AdminRequest(t, app, http.MethodGet,
		"/api/admin/x/fulfill-usps/labels/0000000000", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// Both tokens are cached: a store buying two labels makes two extra calls in
// total, not four.
func TestTokensAreCached(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "US-3", 2500, 10)
	for range 2 {
		result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
		gctest.Ship(t, app, result.Order.ID, "usps", gocommerce.ShipRequest{})
	}

	st.mu.Lock()
	tokens, payments := st.tokens, st.payments
	st.mu.Unlock()
	if tokens != 1 || payments != 1 {
		t.Errorf("minted %d bearer tokens and %d payment authorizations, want 1 of each", tokens, payments)
	}
}

// Postage is bought and the number is real; only the paper is missing. Failing
// the shipment would leave the store paying for postage against an order the
// engine says is unshipped.
func TestAMissingImageStillShips(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.noImage = true })
	product := gctest.CreateProduct(t, app, "US-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "usps", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	if shipment.Tracking == "" {
		t.Error("the shipment lost its tracking number over a missing label")
	}
	if shipment.LabelURL != "" {
		t.Errorf("label = %q, want none — there is nothing stored to serve", shipment.LabelURL)
	}
}

func TestErrorsAreReported(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.failWith = "Invalid destination ZIP Code" })
	product := gctest.CreateProduct(t, app, "US-5", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "usps", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "Invalid destination ZIP Code") {
		t.Errorf("error = %v, want the USPS reason in it", err)
	}
}

func TestSplitZIP(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, zip, plus4 string }{
		{"94117", "94117", ""},
		// USPS rejects "94117-1234" in ZIPCode and takes the halves apart.
		{"94117-1234", "94117", "1234"},
		{"941171234", "94117", "1234"},
		{"SW1A 1AA", "SW1A 1AA", ""},
	} {
		zip, plus4 := splitZIP(tc.in)
		if zip != tc.zip || plus4 != tc.plus4 {
			t.Errorf("splitZIP(%q) = %q, %q; want %q, %q", tc.in, zip, plus4, tc.zip, tc.plus4)
		}
	}
}

func TestPoundsRoundsUp(t *testing.T) {
	t.Parallel()
	for grams, want := range map[int]float64{
		453:  1.00,
		454:  1.01,
		750:  1.66,
		1000: 2.21,
	} {
		if got := pounds(grams); got != want {
			t.Errorf("pounds(%d) = %v, want %v", grams, got, want)
		}
	}
}
