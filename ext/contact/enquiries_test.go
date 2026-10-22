package contact

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A shopper asks about one product. The enquiry lands in the same inbox as the
// contact form's messages, marked as one and carrying the product as it was
// called when they asked; the store's address is told, with the product named.
func TestAShopperAsksAboutAProduct(t *testing.T) {
	app := gctest.New(t, New(Config{NotifyEmail: "owner@example.com", MessagesPerMinute: -1}))
	ctx := context.Background()
	mug := gctest.CreateProduct(t, app, "MUG-BLUE", 1500, 10)
	other := gctest.CreateProduct(t, app, "MUG-RED", 1500, 10)
	price := int64(900)
	draft, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{Title: "Secret mug", Status: gocommerce.ProductDraft, SKU: "MUG-SECRET", PriceMinor: &price})
	if err != nil {
		t.Fatal(err)
	}
	ask := func(in EnquiryInput) int {
		t.Helper()
		return gctest.Request(t, app, http.MethodPost, "/x/contact/enquiries", in).Code
	}

	if code := ask(EnquiryInput{ProductSlug: mug.Slug, Name: "Ann", Email: "Ann@Example.com", Body: "Is it dishwasher safe?"}); code != http.StatusCreated {
		t.Fatalf("enquiry by slug = %d", code)
	}
	qty := 40
	variant := mug.Variants[0].ID
	if code := ask(EnquiryInput{ProductID: &mug.ID, VariantID: &variant, Quantity: &qty, Name: "Bo", Email: "bo@example.com",
		Phone: "+44 20 7946 0000", Body: "A price for forty?"}); code != http.StatusCreated {
		t.Fatalf("enquiry by id with a variant = %d", code)
	}
	if code := ask(EnquiryInput{ProductID: &mug.ID, Name: "Bot", Email: "b@x.io", Body: "buy now", Website: "spam"}); code != http.StatusCreated {
		t.Errorf("honeypot = %d, want the same answer as a real one", code)
	}
	otherVariant := other.Variants[0].ID
	missing := int64(999999)
	zero := 0
	for name, in := range map[string]EnquiryInput{
		"no product":            {Name: "Cy", Email: "cy@example.com", Body: "hi"},
		"unknown product":       {ProductID: &missing, Name: "Cy", Email: "cy@example.com", Body: "hi"},
		"a draft":               {ProductID: &draft.ID, Name: "Cy", Email: "cy@example.com", Body: "hi"},
		"another's variant":     {ProductID: &mug.ID, VariantID: &otherVariant, Name: "Cy", Email: "cy@example.com", Body: "hi"},
		"id and slug disagree":  {ProductID: &other.ID, ProductSlug: mug.Slug, Name: "Cy", Email: "cy@example.com", Body: "hi"},
		"no quantity":           {ProductID: &mug.ID, Quantity: &zero, Name: "Cy", Email: "cy@example.com", Body: "hi"},
		"no address":            {ProductID: &mug.ID, Name: "Cy", Email: "cy", Body: "hi"},
		"nothing to say":        {ProductID: &mug.ID, Name: "Cy", Email: "cy@example.com", Body: " "},
		"a phone that is not":   {ProductID: &mug.ID, Name: "Cy", Email: "cy@example.com", Phone: "call me maybe", Body: "hi"},
		"a message far too big": {ProductID: &mug.ID, Name: "Cy", Email: "cy@example.com", Body: strings.Repeat("x", maxBody+1)},
	} {
		if code := ask(in); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}

	var enquiries []Message
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages?kind=enquiry", nil), &enquiries)
	if len(enquiries) != 2 {
		t.Fatalf("enquiries = %+v, want Ann's and Bo's and not the bot's", enquiries)
	}
	bo, ann := enquiries[0], enquiries[1]
	if ann.Kind != KindEnquiry || ann.Email != "ann@example.com" || ann.Product == nil ||
		ann.Product.ID != mug.ID || ann.Product.Title != mug.Title || ann.Product.Slug != mug.Slug {
		t.Errorf("Ann's enquiry = %+v %+v", ann, ann.Product)
	}
	if p := bo.Product; p == nil || p.VariantID == nil || *p.VariantID != variant || p.VariantTitle != "MUG-BLUE" ||
		p.Quantity == nil || *p.Quantity != 40 || bo.Phone != "+44 20 7946 0000" {
		t.Errorf("Bo's enquiry = %+v %+v", bo, p)
	}

	if rec := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{Name: "Di", Email: "di@example.com", Body: "Opening hours?"}); rec.Code != http.StatusCreated {
		t.Fatalf("contact form = %d", rec.Code)
	}
	var messages []Message
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages?kind=message", nil), &messages)
	if len(messages) != 1 || messages[0].Kind != KindMessage || messages[0].Product != nil {
		t.Errorf("messages = %+v, want Di's alone, with no product", messages)
	}
	var all []Message
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages", nil), &all)
	if len(all) != 3 {
		t.Errorf("everything = %d, want 3", len(all))
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages?kind=complaint", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d", rec.Code)
	}
	var byProduct []Message
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages?q=mug-blue", nil), &byProduct)
	if len(byProduct) != 2 {
		t.Errorf("search by the product's name = %d, want both enquiries", len(byProduct))
	}

	// One announcement per enquiry, to the store's one address, naming the product.
	logged, _, err := app.Notifications().List(ctx, gocommerce.NotificationQuery{Event: "contact.enquiry"})
	if err != nil || len(logged) != 2 {
		t.Fatalf("announcements = %+v, %v", logged, err)
	}
	if d := logged[1].Data; logged[1].To != "owner@example.com" || d["product_title"] != mug.Title || d["from_email"] != "ann@example.com" {
		t.Errorf("Ann's announcement = %s %+v", logged[1].To, d)
	}

	off := map[string]any{"enquiries": false}
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Settings: off}); err != nil {
		t.Fatal(err)
	}
	if code := ask(EnquiryInput{ProductID: &mug.ID, Name: "Ed", Email: "ed@example.com", Body: "hi"}); code != http.StatusNotFound {
		t.Errorf("enquiry while enquiries are off = %d, want 404", code)
	}
	if rec := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{Name: "Ed", Email: "ed@example.com", Body: "hi"}); rec.Code != http.StatusCreated {
		t.Errorf("the contact form with enquiries off = %d, want it still open", rec.Code)
	}
}

// One address gets a few messages a minute through either form, counted
// together, and is then told to wait; an address it has not heard from is not.
func TestTheFormsAreLimitedPerAddress(t *testing.T) {
	app := gctest.New(t, New(Config{MessagesPerMinute: 2}))
	mug := gctest.CreateProduct(t, app, "MUG-LIMIT", 1500, 10)
	post := func(from, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(mustJSON(t, body)))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = from + ":4242"
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	msg := MessageInput{Name: "Ann", Email: "ann@example.com", Body: "hi"}
	enq := EnquiryInput{ProductID: &mug.ID, Name: "Ann", Email: "ann@example.com", Body: "hi"}
	if rec := post("198.51.100.7", "/x/contact/messages", msg); rec.Code != http.StatusCreated {
		t.Fatalf("first = %d", rec.Code)
	}
	if rec := post("198.51.100.7", "/x/contact/enquiries", enq); rec.Code != http.StatusCreated {
		t.Fatalf("second = %d", rec.Code)
	}
	rec := post("198.51.100.7", "/x/contact/messages", msg)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("third = %d (Retry-After %q), want 429 saying when", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := post("198.51.100.8", "/x/contact/enquiries", enq); rec.Code != http.StatusCreated {
		t.Errorf("another address = %d, want its own allowance", rec.Code)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
