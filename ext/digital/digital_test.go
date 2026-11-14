package digital

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/gctest"
)

// prepaid is a way to pay that settles later, by webhook or by an operator,
// and can give the money back: what a gateway is, without the gateway.
type prepaid struct{}

func (prepaid) Code() string { return "prepaid" }
func (prepaid) Initiate(context.Context, *gocommerce.Order, gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	return gocommerce.PaymentIntent{Kind: gocommerce.IntentNone, Provider: "prepaid"}, nil
}
func (prepaid) Refund(context.Context, *gocommerce.Order, int64) error { return nil }

type prepaidModule struct{}

func (prepaidModule) Name() string                       { return "prepaid" }
func (prepaidModule) Migrations() []gocommerce.Migration { return nil }
func (prepaidModule) Register(app *gocommerce.App) error {
	app.RegisterPayment(prepaid{})
	return nil
}

type fixture struct {
	t   *testing.T
	app *gocommerce.App
	m   *Module
}

func newFixture(t *testing.T) *fixture {
	m := New(Config{})
	return &fixture{t: t, app: gctest.New(t, m, prepaidModule{}), m: m}
}

// ebook is a product that is not sent, with one file of the given bytes.
func (f *fixture) ebook(sku string, content []byte) (*gocommerce.Product, File) {
	f.t.Helper()
	no := false
	p, err := f.app.Products().CreateProduct(context.Background(), gocommerce.ProductInput{
		Title: "Guide " + sku, Status: "active",
		Variants: []gocommerce.VariantInput{{SKU: sku, PriceMinor: 49900, RequiresShipping: &no, TrackInventory: &no}},
	})
	if err != nil {
		f.t.Fatalf("create product: %v", err)
	}
	return p, f.upload(http.MethodPost, fmt.Sprintf("/api/admin/x/digital/products/%d/files", p.ID), sku+".pdf", content, http.StatusCreated)
}

func (f *fixture) upload(method, target, name string, content []byte, want int) File {
	f.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", name)
	part.Write(content)
	w.Close()
	rec := gctest.AdminUpload(f.t, f.app, method, target, w.FormDataContentType(), body.String())
	if rec.Code != want {
		f.t.Fatalf("%s %s = %d: %s", method, target, rec.Code, rec.Body)
	}
	var file File
	if want < 300 {
		gctest.DecodeData(f.t, rec, &file)
	}
	return file
}

// buy checks a basket of these variants out by the given method, with only a
// billing country and state, and returns the order as placed.
func (f *fixture) buy(method string, variants ...int64) *gocommerce.Order {
	f.t.Helper()
	ctx := context.Background()
	cart, err := f.app.Cart().Create(ctx, "")
	if err != nil {
		f.t.Fatalf("create cart: %v", err)
	}
	for _, v := range variants {
		if _, err := f.app.Cart().AddLine(ctx, cart.Token, v, 1); err != nil {
			f.t.Fatalf("add to cart: %v", err)
		}
	}
	res, err := f.app.Order().Checkout(ctx, method, gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "buyer@example.com", Name: "A Buyer",
		Address: gocommerce.Address{Country: "IN", State: "MH"},
	}, "")
	if err != nil {
		f.t.Fatalf("checkout: %v", err)
	}
	return res.Order
}

func (f *fixture) pay(o *gocommerce.Order) {
	f.t.Helper()
	if _, err := f.app.Pay().MarkPaid(context.Background(), o.ID, "ref-"+o.Number); err != nil {
		f.t.Fatalf("mark paid: %v", err)
	}
	gctest.DrainOutbox(f.t, f.app)
}

func (f *fixture) downloads(o *gocommerce.Order, token string) (int, []Download) {
	f.t.Helper()
	rec := gctest.Request(f.t, f.app, http.MethodGet,
		"/x/digital/orders/"+url.PathEscape(o.Number)+"?token="+url.QueryEscape(token), nil)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var out struct{ Downloads []Download }
	gctest.DecodeData(f.t, rec, &out)
	return rec.Code, out.Downloads
}

// mint asks for a link and returns its path, or the error code.
func (f *fixture) mint(o *gocommerce.Order, token string, grant int64) (string, string) {
	f.t.Helper()
	rec := gctest.Request(f.t, f.app, http.MethodPost,
		fmt.Sprintf("/x/digital/orders/%s/downloads/%d?token=%s", url.PathEscape(o.Number), grant, url.QueryEscape(token)), nil)
	if rec.Code != http.StatusCreated {
		return "", errCode(f.t, rec)
	}
	var out struct{ URL string }
	gctest.DecodeData(f.t, rec, &out)
	return out.URL, ""
}

func (f *fixture) fetch(path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Code == "" {
		return fmt.Sprintf("status %d: %s", rec.Code, rec.Body)
	}
	return env.Error.Code
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// The whole sale: a file uploaded, bought with no street address, paid, handed
// over, announced, and downloaded — with the count, the range, and the parcel
// the order now shows.
func TestABoughtFileIsDeliveredOnPayment(t *testing.T) {
	f := newFixture(t)
	content := randomBytes(2*chunkSize + 12345) // three chunks, the last one short
	p, file := f.ebook("DG-1", content)
	if file.SizeBytes != int64(len(content)) || file.Filename != "DG-1.pdf" {
		t.Fatalf("stored file = %+v", file)
	}

	o := f.buy("prepaid", p.Variants[0].ID)
	if o.Shipping.AmountMinor != 0 {
		t.Errorf("shipping = %d, want nothing charged for a download", o.Shipping.AmountMinor)
	}
	if _, list := f.downloads(o, o.AccessToken); len(list) != 0 {
		t.Fatalf("an unpaid order already lists %d downloads", len(list))
	}

	f.pay(o)
	_, list := f.downloads(o, o.AccessToken)
	if len(list) != 1 || list[0].Status != "ready" || list[0].Filename != "DG-1.pdf" {
		t.Fatalf("downloads after payment = %+v", list)
	}

	got, err := f.app.Order().Get(context.Background(), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != gocommerce.OrderDelivered {
		t.Errorf("order status = %s, want delivered", got.Status)
	}
	if len(got.Fulfillments) != 1 || got.Fulfillments[0].Provider != ProviderCode {
		t.Errorf("fulfillments = %+v, want one by %s", got.Fulfillments, ProviderCode)
	}

	link, code := f.mint(o, o.AccessToken, list[0].ID)
	if code != "" {
		t.Fatalf("mint = %s", code)
	}
	rec := f.fetch(link)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("download = %d, %d bytes; want the %d bytes uploaded", rec.Code, rec.Body.Len(), len(content))
	}
	for h, want := range map[string]string{
		"Content-Disposition":    `attachment; filename=DG-1.pdf`,
		"Content-Type":           "application/pdf",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "no-store",
	} {
		if got := rec.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}

	// The same link again, for part of the file: the same download, not a second.
	rec = f.fetch(link, "Range", "bytes=0-9")
	if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), content[:10]) {
		t.Errorf("range = %d %q", rec.Code, rec.Body.Bytes())
	}
	if _, list = f.downloads(o, o.AccessToken); list[0].Downloads != 1 {
		t.Errorf("downloads counted = %d, want 1 for one link", list[0].Downloads)
	}

	// No "on its way" for a file; one "downloads ready", which never logs the key.
	shipped := gctest.AdminRequest(t, f.app, http.MethodGet, "/api/admin/notifications?event=order.shipped", nil)
	var rows []gocommerce.NotificationRecord
	gctest.DecodeData(t, shipped, &rows)
	if len(rows) != 0 {
		t.Errorf("%d order.shipped messages for a download, want none", len(rows))
	}
	gctest.AssertNotificationLogHides(t, f.app, EventReady, o.AccessToken)

	// A second delivery of the same event changes nothing.
	if err := f.m.deliver(context.Background(), o.ID); err != nil {
		t.Fatal(err)
	}
	if _, again := f.downloads(o, o.AccessToken); len(again) != 1 {
		t.Errorf("redelivery left %d grants", len(again))
	}
}

// Somebody else's token, a wrong one, or none reaches nothing — not even
// whether the download exists.
func TestAnotherBuyerCannotReachTheFiles(t *testing.T) {
	f := newFixture(t)
	p, _ := f.ebook("DG-2", []byte("the paid content"))
	mine := f.buy("prepaid", p.Variants[0].ID)
	theirs := f.buy("prepaid", p.Variants[0].ID)
	f.pay(mine)
	f.pay(theirs)
	_, list := f.downloads(mine, mine.AccessToken)
	if len(list) != 1 {
		t.Fatalf("downloads = %+v", list)
	}

	if code, _ := f.downloads(mine, theirs.AccessToken); code != http.StatusNotFound {
		t.Errorf("my order with their token = %d, want 404", code)
	}
	if code, _ := f.downloads(mine, ""); code != http.StatusNotFound {
		t.Errorf("my order with no token = %d, want 404", code)
	}
	// Their own order and token, but my grant's id.
	if _, code := f.mint(theirs, theirs.AccessToken, list[0].ID); code != "not_found" {
		t.Errorf("their token minting my grant = %s, want not_found", code)
	}
	if _, code := f.mint(mine, theirs.AccessToken, list[0].ID); code != "not_found" {
		t.Errorf("my order, their token = %s, want not_found", code)
	}
	if rec := f.fetch("/x/digital/download/" + strings.Repeat("0", 64)); rec.Code != http.StatusGone {
		t.Errorf("a made-up link = %d, want 410", rec.Code)
	}
}

// The limit is per file, a link minted after it is refused, and a link dies
// after its minutes; an expired download is refused at the next mint.
func TestLimitsAndExpiry(t *testing.T) {
	f := newFixture(t)
	p, _ := f.ebook("DG-3", []byte("limited"))
	if rec := gctest.AdminRequest(t, f.app, http.MethodPut, fmt.Sprintf("/api/admin/x/digital/products/%d/rule", p.ID),
		map[string]any{"download_limit": 1, "expiry_days": 30}); rec.Code != http.StatusOK {
		t.Fatalf("set rule = %d: %s", rec.Code, rec.Body)
	}
	o := f.buy("prepaid", p.Variants[0].ID)
	f.pay(o)
	_, list := f.downloads(o, o.AccessToken)
	if list[0].Limit == nil || *list[0].Limit != 1 || list[0].ExpiresAt == nil {
		t.Fatalf("grant = %+v, want the product's rule copied", list[0])
	}

	link, _ := f.mint(o, o.AccessToken, list[0].ID)
	if rec := f.fetch(link); rec.Code != http.StatusOK {
		t.Fatalf("first download = %d", rec.Code)
	}
	if _, code := f.mint(o, o.AccessToken, list[0].ID); code != "download_limit_reached" {
		t.Errorf("mint after the limit = %s, want download_limit_reached", code)
	}

	// A fresh order, its link left past its minutes.
	o2 := f.buy("prepaid", p.Variants[0].ID)
	f.pay(o2)
	_, list = f.downloads(o2, o2.AccessToken)
	link, _ = f.mint(o2, o2.AccessToken, list[0].ID)
	f.m.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if rec := f.fetch(link); rec.Code != http.StatusGone || errCode(t, rec) != "download_link_expired" {
		t.Errorf("a stale link = %d %s, want download_link_expired", rec.Code, errCode(t, rec))
	}
	f.m.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	if _, code := f.mint(o2, o2.AccessToken, list[0].ID); code != "download_expired" {
		t.Errorf("mint after the days ran out = %s, want download_expired", code)
	}
}

// A full refund closes the order's downloads, a link already in hand with
// them; an operator can open one again.
func TestRefundRevokes(t *testing.T) {
	f := newFixture(t)
	p, _ := f.ebook("DG-4", []byte("refundable"))
	o := f.buy("prepaid", p.Variants[0].ID)
	f.pay(o)
	_, list := f.downloads(o, o.AccessToken)
	link, _ := f.mint(o, o.AccessToken, list[0].ID)

	paid, err := f.app.Order().Get(context.Background(), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Pay().Refund(context.Background(), o.ID,
		gocommerce.RefundRequest{AmountMinor: paid.Total.AmountMinor, Reason: "changed mind"}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	gctest.DrainOutbox(t, f.app)

	if _, list = f.downloads(o, o.AccessToken); list[0].Status != "revoked" {
		t.Errorf("after a full refund status = %s, want revoked", list[0].Status)
	}
	if rec := f.fetch(link); rec.Code != http.StatusGone {
		t.Errorf("a link minted before the refund = %d, want 410", rec.Code)
	}
	if _, code := f.mint(o, o.AccessToken, list[0].ID); code != "download_revoked" {
		t.Errorf("mint after refund = %s, want download_revoked", code)
	}

	// The money went back, so the file stays closed whoever asks.
	if rec := gctest.AdminRequest(t, f.app, http.MethodPost, fmt.Sprintf("/api/admin/x/digital/grants/%d/restore", list[0].ID), nil); rec.Code != http.StatusConflict {
		t.Errorf("restore on a refunded order = %d, want 409", rec.Code)
	}

	// On a paid order an operator closes one download and opens it again,
	// its count back at zero.
	o2 := f.buy("prepaid", p.Variants[0].ID)
	f.pay(o2)
	_, list = f.downloads(o2, o2.AccessToken)
	grant := fmt.Sprintf("/api/admin/x/digital/grants/%d/", list[0].ID)
	if rec := gctest.AdminRequest(t, f.app, http.MethodPost, grant+"revoke", nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", rec.Code, rec.Body)
	}
	if _, code := f.mint(o2, o2.AccessToken, list[0].ID); code != "download_revoked" {
		t.Errorf("mint after revoke = %s", code)
	}
	if rec := gctest.AdminRequest(t, f.app, http.MethodPost, grant+"restore", nil); rec.Code != http.StatusOK {
		t.Fatalf("restore = %d: %s", rec.Code, rec.Body)
	}
	if _, code := f.mint(o2, o2.AccessToken, list[0].ID); code != "" {
		t.Errorf("mint after restore = %s, want a link", code)
	}
}

// A HEAD on a link is a probe, not a download; a second link minted before
// the first was used is still held to the limit.
func TestProbesAndRacingLinksDoNotBeatTheLimit(t *testing.T) {
	f := newFixture(t)
	p, _ := f.ebook("DG-7", []byte("once only"))
	gctest.AdminRequest(t, f.app, http.MethodPut, fmt.Sprintf("/api/admin/x/digital/products/%d/rule", p.ID),
		map[string]any{"download_limit": 1})
	o := f.buy("prepaid", p.Variants[0].ID)
	f.pay(o)
	_, list := f.downloads(o, o.AccessToken)
	first, _ := f.mint(o, o.AccessToken, list[0].ID)

	req := httptest.NewRequest(http.MethodHead, first, nil)
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" {
		t.Errorf("HEAD = %d, ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
	if _, after := f.downloads(o, o.AccessToken); after[0].Downloads != 0 {
		t.Errorf("a HEAD counted %d downloads", after[0].Downloads)
	}

	second, _ := f.mint(o, o.AccessToken, list[0].ID)
	if rec := f.fetch(second); rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	if rec := f.fetch(first); rec.Code != http.StatusGone {
		t.Errorf("the replaced first link = %d, want 410", rec.Code)
	}
	if _, code := f.mint(o, o.AccessToken, list[0].ID); code != "download_limit_reached" {
		t.Errorf("third mint = %s, want download_limit_reached", code)
	}
}

// Cash on delivery has no delivery to wait for in a basket of downloads; a
// basket with a parcel in it may still pay that way.
func TestCashOnDeliveryIsRefusedForDownloadsOnly(t *testing.T) {
	f := newFixture(t)
	p, _ := f.ebook("DG-5", []byte("x"))
	ctx := context.Background()
	cart, _ := f.app.Cart().Create(ctx, "")
	f.app.Cart().AddLine(ctx, cart.Token, p.Variants[0].ID, 1)
	_, err := f.app.Order().Checkout(ctx, gocommerce.CodeCOD, gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "buyer@example.com", Address: gocommerce.Address{Country: "IN", State: "MH"},
	}, "")
	if err == nil || !strings.Contains(err.Error(), "cash on delivery") {
		t.Errorf("COD for a download = %v, want refused", err)
	}
	// The seller recording a sale paid by UPI goes through, and marks it paid.
	placed, err := f.app.Order().Create(ctx, gocommerce.NewOrderInput{
		Email: "buyer@example.com", Address: gocommerce.Address{Country: "IN", State: "MH"},
		Lines: []gocommerce.NewOrderLine{{VariantID: p.Variants[0].ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("an operator's order for a download: %v", err)
	}
	f.pay(placed.Order)
	if _, list := f.downloads(placed.Order, placed.Order.AccessToken); len(list) != 1 {
		t.Errorf("operator order downloads = %+v", list)
	}

	tee := gctest.CreateProduct(t, f.app, "DG-TEE", 1000, 5)
	mixed, _ := f.app.Cart().Create(ctx, "")
	f.app.Cart().AddLine(ctx, mixed.Token, p.Variants[0].ID, 1)
	f.app.Cart().AddLine(ctx, mixed.Token, tee.Variants[0].ID, 1)
	res, err := f.app.Order().Checkout(ctx, gocommerce.CodeCOD, gocommerce.CheckoutInput{
		CartID: mixed.Token, Email: "buyer@example.com",
		Address: gocommerce.Address{Line1: "1 MG Road", City: "Pune", State: "MH", PostalCode: "411001", Country: "IN"},
	}, "")
	if err != nil {
		t.Fatalf("COD for a mixed basket: %v", err)
	}
	// Paid on delivery: the file opens then, and the tee is left to ship.
	f.pay(res.Order)
	if _, list := f.downloads(res.Order, res.Order.AccessToken); len(list) != 1 {
		t.Errorf("mixed order downloads = %+v", list)
	}
	got, _ := f.app.Order().Get(ctx, res.Order.ID)
	if got.Status != gocommerce.OrderPartial {
		t.Errorf("mixed order status = %s, want partial with the tee still owed", got.Status)
	}
}

// Replacing keeps the file and every grant on it; removing takes it away.
func TestReplaceAndRemove(t *testing.T) {
	f := newFixture(t)
	p, file := f.ebook("DG-6", []byte("first edition"))
	o := f.buy("prepaid", p.Variants[0].ID)
	f.pay(o)

	f.upload(http.MethodPut, fmt.Sprintf("/api/admin/x/digital/files/%d/content", file.ID), "DG-6-v2.pdf", []byte("second edition"), http.StatusOK)
	_, list := f.downloads(o, o.AccessToken)
	link, _ := f.mint(o, o.AccessToken, list[0].ID)
	if rec := f.fetch(link); rec.Body.String() != "second edition" {
		t.Errorf("after replace the buyer gets %q", rec.Body)
	}
	var chunks int
	f.app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM digital_chunks WHERE file_id = $1`, file.ID).Scan(&chunks)
	if chunks != 1 {
		t.Errorf("%d chunk rows after replace, want the old version gone", chunks)
	}

	if rec := gctest.AdminRequest(t, f.app, http.MethodDelete, fmt.Sprintf("/api/admin/x/digital/files/%d", file.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if _, list = f.downloads(o, o.AccessToken); len(list) != 0 {
		t.Errorf("after remove the buyer still has %+v", list)
	}
	f.upload(http.MethodPost, fmt.Sprintf("/api/admin/x/digital/products/%d/files", p.ID), "empty.pdf", nil, http.StatusBadRequest)
}

// A page an operator uploads is served as bytes, never as a page on the
// engine's origin.
func TestActiveContentIsServedAsBytes(t *testing.T) {
	for in, want := range map[string]string{
		"text/html; charset=utf-8": "application/octet-stream",
		"image/svg+xml":            "application/octet-stream",
		"application/javascript":   "application/octet-stream",
		"text/xsl":                 "application/octet-stream",
		"":                         "application/octet-stream",
		"application/pdf":          "application/pdf",
		"application/zip":          "application/zip",
	} {
		if got := servedType(in); got != want {
			t.Errorf("servedType(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		`..\..\etc\passwd`:  "passwd",
		"a/b/guide.pdf":     "guide.pdf",
		"bad\"name\r\n.pdf": "badname.pdf",
		"report‮fdp.exe":    "reportfdp.exe",
		"..":                "",
	} {
		if got := cleanFilename(in); got != want {
			t.Errorf("cleanFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContract(t *testing.T) {
	app := gctest.NewWithReports(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "digital")
	gctest.AssertSpecCoversModuleRoutes(t, app, "digital")
	gctest.AssertReportsCannotRead(t, app, "digital_grants", "link_hash")
}
